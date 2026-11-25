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

	// AgentInboxRoot is the reply-subject root for enrolled agents. Each agent
	// uses AgentInboxPrefix(agentID) and may subscribe only below it, so no
	// agent can read another agent's replies or command deliveries.
	AgentInboxRoot = "_INBOX_AGENT"
	// ControllerInboxPrefix keeps the controller's replies (including
	// JetStream API answers) out of the default _INBOX namespace that
	// enrolling agents may read.
	ControllerInboxPrefix = "_INBOX_CONTROLLER"
	// BootstrapInboxRoot is the default NATS inbox root, used by agents that
	// have no NATS user JWT yet.
	BootstrapInboxRoot = "_INBOX"
)

// AgentInboxPrefix is the inbox prefix an enrolled agent must use.
func AgentInboxPrefix(agentID string) (string, error) {
	if !ValidIdentifier(agentID) {
		return "", fmt.Errorf("agent ID %q is not a valid subject token", agentID)
	}
	return AgentInboxRoot + "." + agentID, nil
}

// Permissions lists the subjects a NATS user may publish to and subscribe to.
type Permissions struct {
	Publish   []string
	Subscribe []string
}

// AgentPermissions are the NATS permissions of an enrolled agent: lease
// renewal and re-registration, its own results, and its own durable command
// consumer (info, pull and acknowledgements). Nothing else.
func AgentPermissions(agentID string) (Permissions, error) {
	result, err := ResultSubject(agentID)
	if err != nil {
		return Permissions{}, err
	}
	consumer, err := CommandConsumer(agentID)
	if err != nil {
		return Permissions{}, err
	}
	inbox, err := AgentInboxPrefix(agentID)
	if err != nil {
		return Permissions{}, err
	}
	return Permissions{
		Publish: []string{
			SubjectRegister,
			SubjectLeaseRenew,
			result,
			"$JS.API.CONSUMER.INFO." + CommandStream + "." + consumer,
			"$JS.API.CONSUMER.MSG.NEXT." + CommandStream + "." + consumer,
			"$JS.ACK." + CommandStream + "." + consumer + ".>",
		},
		Subscribe: []string{inbox + ".>"},
	}, nil
}

// BootstrapPermissions are the NATS permissions of the shared credential an
// agent uses before it has a NATS user JWT: send registration and renewal
// requests and read their replies. Requests are signed, so the credential
// cannot impersonate an agent.
func BootstrapPermissions() Permissions {
	return Permissions{
		Publish:   []string{SubjectRegister, SubjectLeaseRenew},
		Subscribe: []string{BootstrapInboxRoot + ".>"},
	}
}

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
