package agentv1

// The agent gateway transport (ADR-059 in the machinarchy repository) is a
// gRPC service over the controller's TLS endpoint. Message bodies are the
// signed envelopes of this package, carried as opaque bytes, so authenticity
// never depends on the transport.

// GatewayService is the gRPC service name the controller serves.
const GatewayService = "machinarchy.agent.v1.AgentGateway"

// GatewayCodec is the gRPC content subtype: raw bytes, no protobuf schema.
const GatewayCodec = "agentv1"

// Methods of GatewayService.
const (
	// MethodRegister is unary: a signed RegisterRequest, answered with a
	// signed RegisterResponse.
	MethodRegister = "Register"
	// MethodRenew is unary: a signed RenewRequest, answered with a signed
	// RenewResponse.
	MethodRenew = "Renew"
	// MethodSendResult is unary: a signed Result, answered with an empty
	// message. It needs a live lease.
	MethodSendResult = "SendResult"
	// MethodCommands is a bidirectional stream. The agent opens it with a
	// signed RenewRequest naming its current lease, then receives one signed
	// Command at a time and acknowledges each with its command ID.
	MethodCommands = "Commands"
)

// MaxAckBytes bounds one acknowledgement on a Commands stream.
const MaxAckBytes = 128
