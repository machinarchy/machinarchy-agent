package fakecontroller

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// OperatorMode is a NATS operator (JWT) setup for tests: an embedded server
// trusts the operator, one account has JetStream, and its signing key issues
// user JWTs. It mirrors a production deployment created with nsc.
type OperatorMode struct {
	AccountPublicKey string
	AccountSigner    nkeys.KeyPair

	operator   *jwt.OperatorClaims
	accountJWT string
	systemKey  string
	systemJWT  string
}

// NewOperatorMode creates fresh operator, system account and application
// account keys.
func NewOperatorMode() (*OperatorMode, error) {
	operatorKey, err := nkeys.CreateOperator()
	if err != nil {
		return nil, err
	}
	operatorPublic, err := operatorKey.PublicKey()
	if err != nil {
		return nil, err
	}
	account := func(name string, jetStream bool, signer nkeys.KeyPair) (string, string, error) {
		key, err := nkeys.CreateAccount()
		if err != nil {
			return "", "", err
		}
		public, err := key.PublicKey()
		if err != nil {
			return "", "", err
		}
		claims := jwt.NewAccountClaims(public)
		claims.Name = name
		if signer != nil {
			signingPublic, err := signer.PublicKey()
			if err != nil {
				return "", "", err
			}
			claims.SigningKeys.Add(signingPublic)
		}
		if jetStream {
			claims.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
		}
		encoded, err := claims.Encode(operatorKey)
		return public, encoded, err
	}
	systemPublic, systemJWT, err := account("SYS", false, nil)
	if err != nil {
		return nil, err
	}
	signer, err := nkeys.CreateAccount()
	if err != nil {
		return nil, err
	}
	accountPublic, accountJWT, err := account("MACHINARCHY", true, signer)
	if err != nil {
		return nil, err
	}
	operatorClaims := jwt.NewOperatorClaims(operatorPublic)
	operatorClaims.Name = "test-operator"
	operatorClaims.SystemAccount = systemPublic
	encoded, err := operatorClaims.Encode(operatorKey)
	if err != nil {
		return nil, err
	}
	decoded, err := jwt.DecodeOperatorClaims(encoded)
	if err != nil {
		return nil, err
	}
	return &OperatorMode{AccountPublicKey: accountPublic, AccountSigner: signer, operator: decoded,
		accountJWT: accountJWT, systemKey: systemPublic, systemJWT: systemJWT}, nil
}

// Configure makes opts trust the operator and resolve both accounts.
func (o *OperatorMode) Configure(opts *server.Options) error {
	resolver := &server.MemAccResolver{}
	if err := resolver.Store(o.AccountPublicKey, o.accountJWT); err != nil {
		return err
	}
	if err := resolver.Store(o.systemKey, o.systemJWT); err != nil {
		return err
	}
	opts.TrustedOperators = []*jwt.OperatorClaims{o.operator}
	opts.AccountResolver = resolver
	opts.SystemAccount = o.systemKey
	return nil
}

// CredentialsFile writes a NATS creds file for a new user of the account.
// nil permissions mean unrestricted (the controller). allowResponses lets a
// restricted user answer requests it receives; on a user without a publish
// allow list it would limit publishing to replies only, so do not set it for
// unrestricted users.
func (o *OperatorMode) CredentialsFile(dir, name string, permissions *agentv1.Permissions, allowResponses bool) (string, error) {
	key, err := nkeys.CreateUser()
	if err != nil {
		return "", err
	}
	public, err := key.PublicKey()
	if err != nil {
		return "", err
	}
	seed, err := key.Seed()
	if err != nil {
		return "", err
	}
	claims := jwt.NewUserClaims(public)
	claims.Name = name
	claims.IssuerAccount = o.AccountPublicKey
	if permissions != nil {
		claims.Pub.Allow.Add(permissions.Publish...)
		claims.Sub.Allow.Add(permissions.Subscribe...)
	}
	if allowResponses {
		claims.Resp = &jwt.ResponsePermission{MaxMsgs: 1, Expires: time.Minute}
	}
	encoded, err := claims.Encode(o.AccountSigner)
	if err != nil {
		return "", err
	}
	creds, err := jwt.FormatUserConfig(encoded, seed)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".creds")
	return path, os.WriteFile(path, creds, 0o600)
}

// MintAgentJWT issues the NATS user JWT for an enrolled agent: its subject is
// the agent's Ed25519 identity key as a user nkey, and its permissions are
// agentv1.AgentPermissions(agentID).
func MintAgentJWT(signer nkeys.KeyPair, account string, agentKey ed25519.PublicKey, agentID string, ttl time.Duration) (string, error) {
	if len(agentKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("agent key has %d bytes", len(agentKey))
	}
	userKey, err := nkeys.Encode(nkeys.PrefixByteUser, agentKey)
	if err != nil {
		return "", err
	}
	permissions, err := agentv1.AgentPermissions(agentID)
	if err != nil {
		return "", err
	}
	claims := jwt.NewUserClaims(string(userKey))
	claims.Name = agentID
	claims.IssuerAccount = account
	claims.Expires = time.Now().Add(ttl).Unix()
	claims.Pub.Allow.Add(permissions.Publish...)
	claims.Sub.Allow.Add(permissions.Subscribe...)
	return claims.Encode(signer)
}
