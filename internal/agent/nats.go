package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSConnector is the agent-specific NATS transport adapter. It currently
// remains separate from the repository's existing internal/broker package;
// reconcile the adapters before merging so the application does not retain
// duplicate NATS connection abstractions indefinitely.
type NATSConnector struct {
	logger *slog.Logger
}

func NewNATSConnector(logger *slog.Logger) *NATSConnector {
	if logger == nil {
		logger = slog.Default()
	}
	return &NATSConnector{logger: logger.With("component", "agent", "transport", "nats")}
}

func (c *NATSConnector) Connect(cfg Config, events chan<- ConnectionEvent) (Connection, error) {
	if events == nil {
		return nil, fmt.Errorf("broker lifecycle event channel is required")
	}

	emit := func(event ConnectionEvent) {
		select {
		case events <- event:
		default:
			// Lifecycle notifications are advisory. A full channel must never
			// block a NATS callback or the library's connection state machine.
			c.logger.Warn("dropping broker lifecycle notification", "event_type", event.Type)
		}
	}

	options := []nats.Option{
		nats.Name(cfg.ClientName),
		nats.Timeout(cfg.ConnectTimeout),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.ReconnectJitter(cfg.ReconnectJitter, cfg.ReconnectJitter),
		nats.RetryOnFailedConnect(true),
		nats.DrainTimeout(cfg.DrainTimeout),
		nats.PingInterval(20 * time.Second),
		nats.MaxPingsOutstanding(2),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			emit(ConnectionEvent{Type: EventDisconnected, Err: err})
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			emit(ConnectionEvent{Type: EventReconnected})
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			emit(ConnectionEvent{Type: EventClosed})
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			emit(ConnectionEvent{Type: EventAsyncError, Err: err})
		}),
	}

	switch {
	case cfg.CredentialsFile != "":
		path := expandHome(cfg.CredentialsFile)
		options = append(options, nats.UserCredentials(path))
	case cfg.Token != "":
		options = append(options, nats.Token(cfg.Token))
	case cfg.Username != "" && cfg.Password != "":
		options = append(options, nats.UserInfo(cfg.Username, cfg.Password))
	}

	nc, err := nats.Connect(cfg.normalizedBrokerURLs(), options...)
	if err != nil {
		return nil, fmt.Errorf("initialize NATS connection: %w", err)
	}
	return &natsConnection{conn: nc, logger: c.logger}, nil
}

type natsConnection struct {
	conn   *nats.Conn
	logger *slog.Logger
}

func (c *natsConnection) IsConnected() bool {
	return c != nil && c.conn != nil && c.conn.IsConnected()
}

// Request performs a bounded NATS request/reply operation. The agent control
// plane uses this for registration and lease renewal acknowledgements.
func (c *natsConnection) Request(ctx context.Context, subject string, payload []byte) ([]byte, error) {
	if c == nil || c.conn == nil || c.conn.IsClosed() {
		return nil, errors.New("NATS connection is not available")
	}
	if ctx == nil {
		return nil, errors.New("NATS request context is required")
	}
	message, err := c.conn.RequestWithContext(ctx, subject, payload)
	if err != nil {
		return nil, fmt.Errorf("NATS request/reply: %w", err)
	}
	if message == nil {
		return nil, errors.New("NATS request returned an empty message")
	}
	return message.Data, nil
}

// Publish sends an outbound message and flushes it to the connected NATS
// server. A successful flush confirms server receipt, not downstream processing.
func (c *natsConnection) Publish(ctx context.Context, subject string, payload []byte) error {
	if ctx == nil {
		return errors.New("NATS publish context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.conn == nil || c.conn.IsClosed() || !c.conn.IsConnected() {
		return errors.New("NATS connection is not connected")
	}
	if err := validateNATSSubject("publish subject", subject); err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > maxOperationOutputBytes+16*1024 {
		return errors.New("NATS publish payload is empty or exceeds the configured maximum")
	}
	if err := c.conn.Publish(subject, payload); err != nil {
		return fmt.Errorf("publish NATS message: %w", err)
	}
	flushTimeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < flushTimeout {
			flushTimeout = remaining
		}
	}
	if err := c.conn.FlushTimeout(flushTimeout); err != nil {
		return fmt.Errorf("flush NATS publish: %w", err)
	}
	return ctx.Err()
}

// Subscribe creates a bounded asynchronous Core NATS subscription. Both
// pending-message count and pending bytes are capped, and the local delivery
// channel is bounded as well. A slow consumer can trigger a NATS slow-consumer
// error; Core NATS does not provide durable replay or explicit acknowledgements.
func (c *natsConnection) Subscribe(ctx context.Context, subject string, bufferSize int) (EventSubscription, error) {
	if ctx == nil {
		return nil, errors.New("NATS subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.conn == nil || c.conn.IsClosed() || !c.conn.IsConnected() {
		return nil, errors.New("NATS connection is not connected")
	}
	if err := validateNATSSubject("agent event subject", subject); err != nil {
		return nil, err
	}
	if bufferSize < 1 || bufferSize > maxEventBufferSize {
		return nil, fmt.Errorf("agent event buffer size must be between 1 and %d", maxEventBufferSize)
	}

	subCtx, cancel := context.WithCancel(ctx)
	messages := make(chan []byte, bufferSize)
	sub, err := c.conn.Subscribe(subject, func(message *nats.Msg) {
		if message == nil {
			return
		}
		if len(message.Data) > maxAgentEventBytes {
			if c.logger != nil {
				c.logger.Warn("dropping oversized agent event before enqueue", "message_bytes", len(message.Data), "maximum_bytes", maxAgentEventBytes)
			}
			return
		}
		data := append([]byte(nil), message.Data...)
		select {
		case messages <- data:
		case <-subCtx.Done():
		}
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("subscribe to NATS subject %q: %w", subject, err)
	}
	if err := sub.SetPendingLimits(bufferSize, maxPendingAgentEventBytes); err != nil {
		cancel()
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("set bounded NATS pending limits for %q: %w", subject, err)
	}
	// Flush confirms the server has processed the subscription before the
	// caller considers it active.
	if err := c.conn.FlushTimeout(2 * time.Second); err != nil {
		cancel()
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("confirm NATS subscription %q: %w", subject, err)
	}
	return &natsEventSubscription{messages: messages, cancel: cancel, subscription: sub}, nil
}

type natsEventSubscription struct {
	messages     chan []byte
	cancel       context.CancelFunc
	subscription *nats.Subscription
	once         sync.Once
	err          error
}

func (s *natsEventSubscription) Messages() <-chan []byte {
	if s == nil {
		return nil
	}
	return s.messages
}

func (s *natsEventSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.subscription != nil {
			s.err = s.subscription.Unsubscribe()
		}
	})
	return s.err
}

func (c *natsConnection) Drain() error {
	if c == nil || c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	return c.conn.Drain()
}

func (c *natsConnection) Close() {
	if c == nil || c.conn == nil {
		return
	}
	c.conn.Close()
}
