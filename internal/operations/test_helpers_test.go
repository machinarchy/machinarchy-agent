package operations

import (
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/identity"
)

func validTestIdentity() identity.AgentIdentity {
	return identity.AgentIdentity{
		SchemaVersion:           identity.IdentitySchemaVersion,
		AgentID:                 "agent-123",
		HostID:                  "host-456",
		LeaseID:                 "lease-789",
		LeaseToken:              "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt:          time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		HeartbeatIntervalMillis: 15000,
	}
}
