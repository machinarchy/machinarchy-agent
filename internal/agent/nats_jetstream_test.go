package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type fakeJetStreamClient struct {
	consumer jetstream.Consumer
	stream   string
	name     string
	err      error
}

func (f *fakeJetStreamClient) Consumer(_ context.Context, stream, name string) (jetstream.Consumer, error) {
	f.stream, f.name = stream, name
	return f.consumer, f.err
}

type fakeJetStreamConsumer struct {
	info     *jetstream.ConsumerInfo
	iterator jetstream.MessagesContext
	err      error
	batch    int
}

func (f *fakeJetStreamConsumer) Info(context.Context) (*jetstream.ConsumerInfo, error) {
	return f.info, f.err
}
func (f *fakeJetStreamConsumer) Messages(opts ...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	f.batch = len(opts)
	return f.iterator, f.err
}

type fakeJetStreamIterator struct {
	messages chan jetstream.Msg
	stopped  chan struct{}
}

func (f *fakeJetStreamIterator) Next(opts ...jetstream.NextOpt) (jetstream.Msg, error) {
	for _, option := range opts {
		if option == nil {
			continue
		}
	}
	select {
	case msg := <-f.messages:
		return msg, nil
	case <-f.stopped:
		return nil, jetstream.ErrMsgIteratorClosed
	}
}
func (f *fakeJetStreamIterator) Stop() {
	select {
	case <-f.stopped:
	default:
		close(f.stopped)
	}
}

type fakeJetStreamMessage struct {
	data        []byte
	acked       bool
	doubleAcked bool
	nakDelay    time.Duration
	terminated  bool
}

func (m *fakeJetStreamMessage) Data() []byte { return append([]byte(nil), m.data...) }
func (m *fakeJetStreamMessage) DoubleAck(context.Context) error {
	m.acked, m.doubleAcked = true, true
	return nil
}
func (m *fakeJetStreamMessage) NakWithDelay(delay time.Duration) error {
	m.nakDelay = delay
	return nil
}
func (m *fakeJetStreamMessage) Term() error { m.terminated = true; return nil }

func TestNATSSubscribeBindsOnlyToPreProvisionedDurableConsumer(t *testing.T) {
	iterator := &fakeJetStreamIterator{messages: make(chan jetstream.Msg), stopped: make(chan struct{})}
	consumer := &fakeJetStreamConsumer{
		info: &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{
			Durable: "agent_agent-01", FilterSubject: "machinarchy.v1.agent.agent-01.events",
			AckPolicy: jetstream.AckExplicitPolicy,
		}},
		iterator: iterator,
	}
	js := &fakeJetStreamClient{consumer: consumer}
	conn := &natsConnection{
		conn:       &nats.Conn{},
		js:         js,
		streamName: "MACHINARCHY_AGENT_EVENTS",
	}
	// The fake connection is marked as connected by its test-only fields.
	conn.isConnected = func() bool { return true }

	subscription, err := conn.Subscribe(context.Background(), "machinarchy.v1.agent.agent-01.events", "agent_agent-01", 8)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer subscription.Close()
	if js.stream != "MACHINARCHY_AGENT_EVENTS" || js.name != "agent_agent-01" {
		t.Fatalf("consumer binding = %q/%q", js.stream, js.name)
	}
	if consumer.batch != 1 {
		t.Fatalf("Messages() options count = %d, want 1 bounded pull option", consumer.batch)
	}
}

func TestNATSSubscribeRejectsMismatchedConsumer(t *testing.T) {
	conn := &natsConnection{
		conn:       &nats.Conn{},
		js:         &fakeJetStreamClient{consumer: &fakeJetStreamConsumer{info: &jetstream.ConsumerInfo{Config: jetstream.ConsumerConfig{Durable: "wrong", AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: "other"}}}},
		streamName: "MACHINARCHY_AGENT_EVENTS",
	}
	conn.isConnected = func() bool { return true }
	if _, err := conn.Subscribe(context.Background(), "machinarchy.v1.agent.agent-01.events", "agent_agent-01", 8); err == nil {
		t.Fatal("Subscribe() accepted a consumer for a different identity/subject")
	}
}

func TestNATSJetStreamMessageAcknowledgementMethods(t *testing.T) {
	ctx := context.Background()
	message := &fakeJetStreamMessage{data: []byte("event")}
	wrapped := &natsJetStreamEventMessage{message: message, payload: []byte("event")}
	if err := wrapped.Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if !message.acked || !message.doubleAcked {
		t.Fatal("Ack() did not wait for broker confirmation")
	}

	message = &fakeJetStreamMessage{}
	wrapped = &natsJetStreamEventMessage{message: message}
	if err := wrapped.Nak(ctx, 3*time.Second); err != nil {
		t.Fatalf("Nak() error = %v", err)
	}
	if message.nakDelay != 3*time.Second {
		t.Fatalf("Nak() delay = %s", message.nakDelay)
	}
	if err := wrapped.Term(ctx); err != nil || !message.terminated {
		t.Fatalf("Term() = %v, terminated=%v", err, message.terminated)
	}
}

func TestNATSSubscribeRequiresLiveJetStreamConnection(t *testing.T) {
	conn := &natsConnection{js: &fakeJetStreamClient{err: errors.New("not found")}, streamName: "MACHINARCHY_AGENT_EVENTS"}
	if _, err := conn.Subscribe(context.Background(), "machinarchy.v1.agent.agent-01.events", "agent_agent-01", 8); err == nil {
		t.Fatal("Subscribe() succeeded without a live NATS connection")
	}
}
