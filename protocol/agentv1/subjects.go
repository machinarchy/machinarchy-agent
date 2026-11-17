package agentv1

import "fmt"

// Subject layout. The root intentionally differs from the controller's
// internal "machinarchy." namespace so no controller stream can capture
// agent request/reply traffic.
const (
	SubjectRoot           = "machinarchy-agent.v1"
	SubjectRegister       = SubjectRoot + ".register"
	SubjectLeaseRenew     = SubjectRoot + ".lease.renew"
	SubjectAllCommands    = SubjectRoot + ".agent.*.cmd"
	SubjectAllResults     = SubjectRoot + ".agent.*.result"
	CommandStream         = "MACHINARCHY_AGENT_COMMANDS"
	commandConsumerPrefix = "agent-"
)

// CommandSubject is the per-agent JetStream subject for controller commands.
func CommandSubject(agentID string) (string, error) {
	if !ValidIdentifier(agentID) {
		return "", fmt.Errorf("agent ID %q is not a valid subject token", agentID)
	}
	return SubjectRoot + ".agent." + agentID + ".cmd", nil
}

// ResultSubject is the per-agent subject for agent results.
func ResultSubject(agentID string) (string, error) {
	if !ValidIdentifier(agentID) {
		return "", fmt.Errorf("agent ID %q is not a valid subject token", agentID)
	}
	return SubjectRoot + ".agent." + agentID + ".result", nil
}

// CommandConsumer is the durable pull consumer the controller provisions for
// each agent on CommandStream, filtered to CommandSubject(agentID) with
// explicit acknowledgement.
func CommandConsumer(agentID string) (string, error) {
	if !ValidIdentifier(agentID) || len(agentID)+len(commandConsumerPrefix) > MaxIdentifierLength {
		return "", fmt.Errorf("agent ID %q cannot form a consumer name", agentID)
	}
	return commandConsumerPrefix + agentID, nil
}
