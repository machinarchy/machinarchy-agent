// Package transport defines the narrow transport contracts consumed by the
// Agent application and implemented by concrete transport adapters.
package transport

import "github.com/machinarchy/machinarchy-agent/internal/config"

// Connection is the transport lifecycle required by the Agent runtime.
type Connection interface {
	IsConnected() bool
	Drain() error
	Close()
}

// Connector establishes an outbound broker connection and reports lifecycle
// events. Implementations must not log credentials or raw configuration.
type Connector interface {
	Connect(cfg config.Config, events chan<- ConnectionEvent) (Connection, error)
}

type ConnectionEventType string

const (
	EventDisconnected ConnectionEventType = "disconnected"
	EventReconnected  ConnectionEventType = "reconnected"
	EventClosed       ConnectionEventType = "closed"
	EventAsyncError   ConnectionEventType = "async_error"
)

// ConnectionEvent contains transport lifecycle information only. Message
// payloads and credentials are intentionally not part of this type.
type ConnectionEvent struct {
	Type ConnectionEventType
	Err  error
}
