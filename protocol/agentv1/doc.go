// Package agentv1 is the versioned wire contract between the Machinarchy
// controller and machinarchy-agent.
//
// It is the single source of truth for subjects, message bodies, capability
// payloads and the signing scheme. It depends only on the Go standard library
// so the controller can import it without pulling in agent internals.
//
// # Transport
//
// All traffic uses NATS under the subject root "machinarchy-agent.v1", which is
// deliberately outside the controller's "machinarchy.>" namespace:
//
//	machinarchy-agent.v1.register            agent -> controller, request/reply
//	machinarchy-agent.v1.lease.renew         agent -> controller, request/reply
//	machinarchy-agent.v1.agent.<id>.cmd      controller -> agent, JetStream (stream MACHINARCHY_AGENT_COMMANDS)
//	machinarchy-agent.v1.agent.<id>.result   agent -> controller, core NATS publish
//
// # Authenticity
//
// Every message is an [Envelope] carrying the exact body bytes and an Ed25519
// signature over a domain-separated signing input. The controller signs
// register/renew responses and commands with its key, which every agent pins
// in configuration. Each agent generates its own key at first start, proves
// possession by signing its registration request, and signs every later
// request and result. Broker ACLs remain required, but authenticity does not
// depend on them.
package agentv1
