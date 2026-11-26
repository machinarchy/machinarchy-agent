// Package agentv1 is the versioned wire contract between the Machinarchy
// controller and machinarchy-agent.
//
// It is the single source of truth for message bodies, capability
// payloads and the signing scheme. It depends only on the Go standard library
// so the controller can import it without pulling in agent internals.
//
// # Transport
//
// The contract is transport-neutral: signed envelopes only. Agents reach the
// controller through its agent gateway, a gRPC service over TLS (see
// gateway.go for the service, methods and codec). Agents hold no broker
// address or credential; the controller's internal bus is not part of this
// contract.
//
// # Authenticity
//
// Every message is an [Envelope] carrying the exact body bytes and an Ed25519
// signature over a domain-separated signing input. The controller signs
// register/renew responses and commands with its key, which every agent pins
// in configuration. Each agent generates its own key at first start, proves
// possession by signing its registration request, and signs every later
// request and result. Authenticity does not depend on the transport.
package agentv1
