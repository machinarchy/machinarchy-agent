//go:build !linux

package capabilities

import "github.com/machinarchy/machinarchy-agent/protocol/agentv1"

// collectPlatformFacts reports only the portable baseline outside Linux; the
// agent's supported production platform is Linux.
func collectPlatformFacts(*agentv1.SystemFacts) {}
