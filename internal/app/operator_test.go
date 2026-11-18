package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/fakecontroller"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// operatorEnv runs NATS in operator (JWT) mode: the controller has
// unrestricted credentials, agents get only the bootstrap credential and
// receive their own JWT at enrolment.
type operatorEnv struct {
	*env
	mode      *fakecontroller.OperatorMode
	bootstrap string
	dir       string
}

func newOperatorEnv(t *testing.T, jwtTTL time.Duration) *operatorEnv {
	t.Helper()
	mode, err := fakecontroller.NewOperatorMode()
	if err != nil {
		t.Fatal(err)
	}
	opts := &server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true}
	if err := mode.Configure(opts); err != nil {
		t.Fatal(err)
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	t.Cleanup(ns.Shutdown)

	dir := t.TempDir()
	controllerCreds, err := mode.CredentialsFile(dir, "controller", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPermissions := agentv1.BootstrapPermissions()
	bootstrap, err := mode.CredentialsFile(dir, "bootstrap", &bootstrapPermissions, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controller, err := fakecontroller.Start(ctx, ns.ClientURL(), controllerKey(1), enrollmentToken, nats.UserCredentials(controllerCreds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	controller.IssueNATSCredentials(&fakecontroller.NATSIssuer{Signer: mode.AccountSigner, Account: mode.AccountPublicKey, TTL: jwtTTL})
	t.Cleanup(func() {
		for {
			select {
			case err := <-controller.Errors():
				t.Errorf("fake controller reported: %v", err)
			default:
				return
			}
		}
	})
	return &operatorEnv{
		env:  &env{t: t, url: ns.ClientURL(), controller: controller, stateFile: filepath.Join(t.TempDir(), "state", "state.json")},
		mode: mode, bootstrap: bootstrap, dir: dir,
	}
}

func (e *operatorEnv) agentConfig() config.Config {
	return e.config(func(c *config.Config) { c.NATS.CredentialsFile = e.bootstrap })
}

func storedJWT(t *testing.T, path string) string {
	t.Helper()
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := store.Snapshot().Identity
	if identity == nil {
		t.Fatal("no identity stored")
	}
	return identity.NATSUserJWT
}

func TestOperatorModeAgentUsesItsOwnCredential(t *testing.T) {
	// A short JWT lifetime makes the server close the agent's connection
	// while it runs; the agent must reconnect with the renewed JWT.
	// The heartbeat (5s, the protocol minimum) refreshes the JWT before the
	// 8s lifetime of the one the connection started with runs out.
	e := newOperatorEnv(t, 8*time.Second)
	r := e.start(e.agentConfig())
	agentID := e.waitServing(t)

	first := storedJWT(t, e.stateFile)
	if first == "" {
		t.Fatal("the agent did not store its NATS user JWT")
	}
	time.Sleep(10 * time.Second) // past the first JWT's expiry
	e.waitServing(t)
	if storedJWT(t, e.stateFile) == first {
		t.Fatal("renewals did not refresh the stored JWT")
	}
	if err := r.stop(t); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	// Restart after the stored JWT has expired: bootstrap, renew, switch.
	time.Sleep(9 * time.Second)
	expires, err := jwtExpiry(storedJWT(t, e.stateFile))
	if err != nil || time.Now().Before(expires) {
		t.Fatalf("stored JWT should be expired (exp %v, err %v)", expires, err)
	}
	e.start(e.agentConfig())
	if got := e.waitServing(t); got != agentID {
		t.Fatalf("agent ID changed from %s to %s", agentID, got)
	}
}

// natsUser connects as a user with the given JWT and Ed25519 key, collecting
// asynchronous permission errors.
func natsUser(t *testing.T, url, token string, seed []byte, inbox string) (*nats.Conn, <-chan error) {
	t.Helper()
	key, err := nkeys.FromRawSeed(nkeys.PrefixByteUser, seed)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 16)
	options := []nats.Option{
		nats.UserJWT(func() (string, error) { return token, nil }, key.Sign),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errs <- err }),
	}
	if inbox != "" {
		options = append(options, nats.CustomInboxPrefix(inbox))
	}
	nc, err := nats.Connect(url, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc, errs
}

func expectViolation(t *testing.T, what string, nc *nats.Conn, errs <-chan error, act func() error) {
	t.Helper()
	if err := act(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("%s: %v", what, err)
	}
	_ = nc.Flush()
	select {
	case err := <-errs:
		if !errors.Is(err, nats.ErrPermissionViolation) && !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
			t.Fatalf("%s: unexpected error %v", what, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s was allowed", what)
	}
}

// Each per-agent JWT allows only that agent's subjects; the bootstrap
// credential allows only enrolment traffic.
func TestOperatorModePermissions(t *testing.T) {
	e := newOperatorEnv(t, time.Hour)
	e.start(e.agentConfig())
	victim := e.waitServing(t)

	// A second, enrolled agent identity.
	seed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	otherKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	token, err := fakecontroller.MintAgentJWT(e.mode.AccountSigner, e.mode.AccountPublicKey, otherKey, "agent-other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := agentv1.AgentInboxPrefix("agent-other")
	other, otherErrs := natsUser(t, e.url, token, seed, inbox)

	victimResult, _ := agentv1.ResultSubject(victim)
	victimConsumer, _ := agentv1.CommandConsumer(victim)
	victimInbox, _ := agentv1.AgentInboxPrefix(victim)
	for what, act := range map[string]func() error{
		"read another agent's inbox": func() error { _, err := other.SubscribeSync(victimInbox + ".>"); return err },
		"read the register subject":  func() error { _, err := other.SubscribeSync(agentv1.SubjectRegister); return err },
		"read every result":          func() error { _, err := other.SubscribeSync(agentv1.SubjectAllResults); return err },
		"report for another agent":   func() error { return other.Publish(victimResult, []byte("x")) },
		"pull another agent's commands": func() error {
			return other.Publish("$JS.API.CONSUMER.MSG.NEXT."+agentv1.CommandStream+"."+victimConsumer, nil)
		},
		"publish a command":                 func() error { return other.Publish("machinarchy-agent.v1.agent."+victim+".cmd", []byte("x")) },
		"use the JetStream API for streams": func() error { return other.Publish("$JS.API.STREAM.LIST", nil) },
	} {
		t.Run("agent: "+what, func(t *testing.T) { expectViolation(t, what, other, otherErrs, act) })
	}

	// Its own subjects are allowed: a renew request is answered (with an
	// error, since this agent never enrolled with the fake controller, or
	// not at all; either way the publish must not be a violation).
	if err := other.Publish(agentv1.SubjectLeaseRenew, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	_ = other.Flush()
	select {
	case err := <-otherErrs:
		t.Fatalf("own renew subject denied: %v", err)
	case <-time.After(500 * time.Millisecond):
	}

	bootstrapErrs := make(chan error, 16)
	bootstrap, err := nats.Connect(e.url, nats.UserCredentials(e.bootstrap),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { bootstrapErrs <- err }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bootstrap.Close)
	for what, act := range map[string]func() error{
		"read enrolment requests (tokens)": func() error { _, err := bootstrap.SubscribeSync(agentv1.SubjectRegister); return err },
		"read agent inboxes":               func() error { _, err := bootstrap.SubscribeSync(agentv1.AgentInboxRoot + ".>"); return err },
		"read controller replies":          func() error { _, err := bootstrap.SubscribeSync(agentv1.ControllerInboxPrefix + ".>"); return err },
		"report a result":                  func() error { return bootstrap.Publish(victimResult, []byte("x")) },
		"pull commands": func() error {
			return bootstrap.Publish("$JS.API.CONSUMER.MSG.NEXT."+agentv1.CommandStream+"."+victimConsumer, nil)
		},
	} {
		t.Run("bootstrap: "+what, func(t *testing.T) { expectViolation(t, what, bootstrap, bootstrapErrs, act) })
	}
}
