package agentv1

const (
	AgentEventsStreamName    = "MACHINARCHY_AGENT_EVENTS"
	AgentEventsStreamSubject = "machinarchy.v1.agent.*.events"

	SubjectRegisterAgent                 = "machinarchy.v1.agent.register"
	SubjectRenewLease                    = "machinarchy.v1.agent.lease.renew"
	SubjectAgentOperationResultsWildcard = "machinarchy.v1.agent.*.operations.results"
)
