package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/events"
	agenttransport "github.com/machinarchy/machinarchy-agent/internal/transport"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// NATSConnector adapts the NATS client to the Agent's narrow transport
// interfaces. Controller-side broker provisioning and adapters live outside
// this standalone Agent repository.
type NATSConnector struct {
	logger *slog.Logger
}

func NewNATSConnector(logger *slog.Logger) *NATSConnector {
	if logger == nil {
		logger = slog.Default()
	}
	return &NATSConnector{logger: logger.With("component", "agent", "transport", "nats")}
}

func (c *NATSConnector) Connect(cfg config.Config, lifecycle chan<- agenttransport.ConnectionEvent) (agenttransport.Connection, error) {
	if lifecycle == nil {
		return nil, fmt.Errorf("broker lifecycle event channel is required")
	}

	emit := func(event agenttransport.ConnectionEvent) {
		select {
		case lifecycle <- event:
		default:
			// Lifecycle notifications are advisory. A full channel must never
			// block a NATS callback or the library's connection state machine.
			c.logger.Warn("dropping broker lifecycle notification", "event_type", event.Type)
		}
	}

	options := []natsclient.Option{
		natsclient.Name(cfg.ClientName),
		natsclient.Timeout(cfg.ConnectTimeout),
		natsclient.MaxReconnects(-1),
		natsclient.ReconnectWait(cfg.ReconnectWait),
		natsclient.ReconnectJitter(cfg.ReconnectJitter, cfg.ReconnectJitter),
		natsclient.RetryOnFailedConnect(true),
		natsclient.DrainTimeout(cfg.DrainTimeout),
		natsclient.PingInterval(20 * time.Second),
		natsclient.MaxPingsOutstanding(2),
		natsclient.DisconnectErrHandler(func(_ *natsclient.Conn, err error) {
			emit(agenttransport.ConnectionEvent{Type: agenttransport.EventDisconnected, Err: err})
		}),
		natsclient.ReconnectHandler(func(_ *natsclient.Conn) {
			emit(agenttransport.ConnectionEvent{Type: agenttransport.EventReconnected})
		}),
		natsclient.ClosedHandler(func(_ *natsclient.Conn) {
			emit(agenttransport.ConnectionEvent{Type: agenttransport.EventClosed})
		}),
		natsclient.ErrorHandler(func(_ *natsclient.Conn, _ *natsclient.Subscription, err error) {
			emit(agenttransport.ConnectionEvent{Type: agenttransport.EventAsyncError, Err: err})
		}),
	}

	switch {
	case cfg.CredentialsFile != "":
		path := config.ExpandHome(cfg.CredentialsFile)
		options = append(options, natsclient.UserCredentials(path))
	case cfg.Token != "":
		options = append(options, natsclient.Token(cfg.Token))
	case cfg.Username != "" && cfg.Password != "":
		options = append(options, natsclient.UserInfo(cfg.Username, cfg.Password))
	}

	nc, err := natsclient.Connect(cfg.NormalizedBrokerURLs(), options...)
	if err != nil {
		return nil, fmt.Errorf("initialize NATS connection: %w", err)
	}
	js, err := jetstream.New(nc, jetstream.WithDefaultTimeout(cfg.ConnectTimeout))
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("initialize NATS JetStream client: %w", err)
	}
	return &natsConnection{conn: nc, js: js, streamName: cfg.JetStreamStream, logger: c.logger}, nil
}

type natsConnection struct {
	conn        *natsclient.Conn
	js          jetstream.JetStream
	streamName  string
	logger      *slog.Logger
	isConnected func() bool // Optional probe used by deterministic transport tests.
}

func (c *natsConnection) IsConnected() bool {
	if c == nil {
		return false
	}
	if c.isConnected != nil {
		return c.isConnected()
	}
	return c.conn != nil && c.conn.IsConnected()
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
	if err := config.ValidateNATSSubject("publish subject", subject); err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > config.MaxOperationOutputBytes+16*1024 {
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

// Subscribe binds to a controller-provisioned durable pull consumer. The Agent
// intentionally does not create or update JetStream streams/consumers: broker
// administration belongs to the controller. Each delivered message remains
// pending until the session acknowledges it after durable local acceptance.
func (c *natsConnection) Subscribe(ctx context.Context, subject, durableName string, bufferSize int) (events.EventSubscription, error) {
	if ctx == nil {
		return nil, errors.New("NATS subscription context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.conn == nil || !c.IsConnected() {
		return nil, errors.New("NATS connection is not connected")
	}
	if c.js == nil {
		return nil, errors.New("NATS JetStream client is not initialized")
	}
	if err := config.ValidateNATSSubject("agent event subject", subject); err != nil {
		return nil, err
	}
	if !config.ValidJetStreamStreamName(durableName) {
		return nil, errors.New("agent event durable consumer name is invalid")
	}
	if bufferSize < 1 || bufferSize > events.MaxEventBufferSize {
		return nil, fmt.Errorf("agent event buffer size must be between 1 and %d", events.MaxEventBufferSize)
	}

	consumer, err := c.js.Consumer(ctx, c.streamName, durableName)
	if err != nil {
		return nil, fmt.Errorf("bind to pre-provisioned agent event consumer %q: %w", durableName, err)
	}
	info, err := consumer.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect agent event consumer %q: %w", durableName, err)
	}
	if info.Config.FilterSubject != subject || info.Config.AckPolicy != jetstream.AckExplicitPolicy || info.Config.Durable != durableName {
		return nil, fmt.Errorf("agent event consumer %q does not match required subject and explicit-ack configuration", durableName)
	}

	iterator, err := consumer.Messages(jetstream.PullMaxMessages(bufferSize))
	if err != nil {
		return nil, fmt.Errorf("start durable pull consumer %q: %w", durableName, err)
	}
	subCtx, cancel := context.WithCancel(ctx)
	subscription := &natsEventSubscription{
		messages: make(chan events.EventMessage, bufferSize),
		cancel:   cancel,
		iterator: iterator,
		done:     make(chan struct{}),
	}
	go subscription.read(subCtx, iterator, c.logger)
	return subscription, nil
}

type natsJetStreamEventMessage struct {
	message jetstream.Msg
	payload []byte
}

func (m *natsJetStreamEventMessage) Payload() []byte {
	if m == nil {
		return nil
	}
	return append([]byte(nil), m.payload...)
}

func (m *natsJetStreamEventMessage) Ack(ctx context.Context) error {
	if m == nil || m.message == nil {
		return errors.New("JetStream event message is nil")
	}
	if ctx == nil {
		return errors.New("JetStream acknowledgement context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.message.DoubleAck(ctx)
}

func (m *natsJetStreamEventMessage) Nak(ctx context.Context, delay time.Duration) error {
	if m == nil || m.message == nil {
		return errors.New("JetStream event message is nil")
	}
	if ctx == nil {
		return errors.New("JetStream negative acknowledgement context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return errors.New("JetStream negative acknowledgement delay must be positive")
	}
	return m.message.NakWithDelay(delay)
}

func (m *natsJetStreamEventMessage) Term(ctx context.Context) error {
	if m == nil || m.message == nil {
		return errors.New("JetStream event message is nil")
	}
	if ctx == nil {
		return errors.New("JetStream termination context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.message.Term()
}

type natsEventSubscription struct {
	messages chan events.EventMessage
	cancel   context.CancelFunc
	iterator jetstream.MessagesContext
	done     chan struct{}
	once     sync.Once
}

func (s *natsEventSubscription) read(ctx context.Context, iterator jetstream.MessagesContext, logger *slog.Logger) {
	defer close(s.done)
	defer close(s.messages)
	for {
		message, err := iterator.Next(jetstream.NextContext(ctx))
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return
			}
			if logger != nil {
				logger.Warn("JetStream event iterator reported an error; continuing durable delivery", "error", err)
			}
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if message == nil {
			continue
		}
		payload := append([]byte(nil), message.Data()...)
		if len(payload) == 0 || len(payload) > events.MaxAgentEventBytes {
			// Oversized or empty messages are poison messages for this protocol.
			// Term them so a malformed publish cannot permanently block the
			// durable consumer. Protocol validation still happens in the session.
			if err := message.Term(); err != nil && logger != nil {
				logger.Warn("failed to terminate invalid-sized JetStream event", "error", err)
			}
			continue
		}
		delivery := &natsJetStreamEventMessage{message: message, payload: payload}
		select {
		case s.messages <- delivery:
		case <-ctx.Done():
			// Leave this delivery unacknowledged. JetStream will redeliver it
			// after the consumer's AckWait interval.
			return
		}
	}
}

func (s *natsEventSubscription) Messages() <-chan events.EventMessage {
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
		if s.iterator != nil {
			s.iterator.Stop()
		}
	})
	if s.done != nil {
		<-s.done
	}
	return nil
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
