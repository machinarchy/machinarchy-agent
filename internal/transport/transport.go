// Package transport is how the agent reaches the controller: a gRPC client of
// the agent gateway (ADR-059 in the machinarchy repository). The agent holds
// only the server URL, its enrolment token and its own keys; there is no
// broker address or credential.
//
// Message bodies are the signed agentv1 envelopes, carried as opaque bytes
// (see codec.go). Verification stays in the caller, so authenticity does not
// depend on this package.
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Client is the agent's view of the controller.
type Client interface {
	// Register sends a signed registration and returns the signed answer.
	Register(ctx context.Context, payload []byte) ([]byte, error)
	// Renew sends a signed lease renewal and returns the signed answer.
	Renew(ctx context.Context, payload []byte) ([]byte, error)
	// SendResult delivers a signed result.
	SendResult(ctx context.Context, payload []byte) error
	// Commands opens the command stream with a signed, lease-bound request.
	Commands(ctx context.Context, open []byte) (CommandStream, error)
	// Connected reports whether the connection to the controller is up.
	Connected() bool
	// Close releases the connection.
	Close() error
}

// CommandStream delivers signed commands one at a time.
type CommandStream interface {
	// Recv blocks for the next command.
	Recv() ([]byte, error)
	// Ack acknowledges the last received command; the controller then sends
	// the next. A command that is not acknowledged is redelivered.
	Ack(commandID string) error
	// Close ends the stream.
	Close()
}

// Options configure Dial.
type Options struct {
	// ServerURL is https://host[:port]. http:// is accepted only with
	// AllowPlaintext (development).
	ServerURL string
	// CAFile adds trusted roots to the system pool.
	CAFile string
	// AllowPlaintext permits http:// URLs.
	AllowPlaintext bool
	// UserAgent identifies the agent version.
	UserAgent string
	// OnDisconnect and OnReconnect observe the connection (optional).
	OnDisconnect func()
	OnReconnect  func()
}

// ParseServerURL validates a server URL and returns the dial target and
// whether TLS is used. TLS verification is never skipped.
func ParseServerURL(raw string, allowPlaintext bool) (target string, useTLS bool, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false, errors.New("server URL must be https://host[:port] without credentials or path")
	}
	switch u.Scheme {
	case "https":
		useTLS = true
	case "http":
		if !allowPlaintext {
			return "", false, errors.New("server URL must use https")
		}
	default:
		return "", false, errors.New("server URL must be https://host[:port]")
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if !useTLS {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), useTLS, nil
}

type client struct {
	conn *grpc.ClientConn
	up   atomic.Bool
	stop context.CancelFunc
}

var _ Client = (*client)(nil)

// Dial prepares the connection. It does not block: the channel connects in the
// background and every call fails fast while the controller is unreachable.
func Dial(opts Options) (Client, error) {
	target, useTLS, err := ParseServerURL(opts.ServerURL, opts.AllowPlaintext)
	if err != nil {
		return nil, err
	}
	var creds credentials.TransportCredentials
	if useTLS {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		if opts.CAFile != "" {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			pem, err := os.ReadFile(opts.CAFile) // #nosec G304 -- operator-configured path.
			if err != nil {
				return nil, fmt.Errorf("read server CA file: %w", err)
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("server CA file holds no certificate")
			}
			tlsConfig.RootCAs = pool
		}
		creds = credentials.NewTLS(tlsConfig)
	} else {
		creds = insecure.NewCredentials()
	}
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 20 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(agentv1.MaxCommandMessageBytes), grpc.MaxCallSendMsgSize(agentv1.MaxResultMessageBytes)),
	}
	if opts.UserAgent != "" {
		dialOptions = append(dialOptions, grpc.WithUserAgent(opts.UserAgent))
	}
	conn, err := grpc.NewClient(target, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("configure controller connection: %w", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	c := &client{conn: conn, stop: stop}
	go c.watch(ctx, opts)
	conn.Connect()
	return c, nil
}

// watch follows the channel state so the agent can report connectivity.
func (c *client) watch(ctx context.Context, opts Options) {
	everUp := false
	for {
		state := c.conn.GetState()
		switch state {
		case connectivity.Ready:
			if !c.up.Swap(true) && everUp && opts.OnReconnect != nil {
				opts.OnReconnect()
			}
			everUp = true
		case connectivity.Shutdown:
			return
		default:
			if c.up.Swap(false) && opts.OnDisconnect != nil {
				opts.OnDisconnect()
			}
			if state == connectivity.Idle {
				c.conn.Connect()
			}
		}
		if !c.conn.WaitForStateChange(ctx, state) {
			return
		}
	}
}

func (c *client) Connected() bool { return c.up.Load() }

func (c *client) Close() error {
	c.stop()
	return c.conn.Close()
}

func method(name string) string { return "/" + agentv1.GatewayService + "/" + name }

func (c *client) unary(ctx context.Context, name string, payload []byte) ([]byte, error) {
	var out frame
	if err := c.conn.Invoke(ctx, method(name), &frame{data: payload}, &out, forceCodec()); err != nil {
		return nil, Error(err)
	}
	return out.data, nil
}

func (c *client) Register(ctx context.Context, payload []byte) ([]byte, error) {
	return c.unary(ctx, agentv1.MethodRegister, payload)
}

func (c *client) Renew(ctx context.Context, payload []byte) ([]byte, error) {
	return c.unary(ctx, agentv1.MethodRenew, payload)
}

func (c *client) SendResult(ctx context.Context, payload []byte) error {
	_, err := c.unary(ctx, agentv1.MethodSendResult, payload)
	return err
}

type commandStream struct {
	stream grpc.ClientStream
	cancel context.CancelFunc
}

func (c *client) Commands(ctx context.Context, open []byte) (CommandStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.conn.NewStream(streamCtx, &grpc.StreamDesc{StreamName: agentv1.MethodCommands, ServerStreams: true, ClientStreams: true},
		method(agentv1.MethodCommands), forceCodec())
	if err != nil {
		cancel()
		return nil, Error(err)
	}
	if err := stream.SendMsg(&frame{data: open}); err != nil {
		cancel()
		return nil, Error(err)
	}
	return &commandStream{stream: stream, cancel: cancel}, nil
}

func (s *commandStream) Recv() ([]byte, error) {
	var in frame
	if err := s.stream.RecvMsg(&in); err != nil {
		return nil, Error(err)
	}
	return in.data, nil
}

func (s *commandStream) Ack(commandID string) error {
	if commandID == "" || len(commandID) > agentv1.MaxAckBytes {
		commandID = "discarded" // still a valid acknowledgement of the delivery
	}
	return Error(s.stream.SendMsg(&frame{data: []byte(commandID)}))
}

func (s *commandStream) Close() { s.cancel() }

// RejectedError is a call the controller refused for authentication or lease
// reasons; retrying with the same credentials cannot succeed until the lease
// is renewed.
type RejectedError struct{ err error }

func (e RejectedError) Error() string { return e.err.Error() }
func (e RejectedError) Unwrap() error { return e.err }

// Error classifies a gRPC error. Messages carry no request content.
func Error(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return RejectedError{err: err}
	}
	return err
}
