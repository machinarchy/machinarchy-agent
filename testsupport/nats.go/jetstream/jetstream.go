// Package jetstream is a compile-time test double for isolated Agent tests.
// It intentionally implements only the API surface used by the Agent. It is
// not a substitute for testing against the real NATS Go client or nats-server.
package jetstream

import (
	"context"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
)

var ErrMsgIteratorClosed = errors.New("message iterator closed")

type JetStreamOpt func(*Options)
type Options struct{ DefaultTimeout time.Duration }

func WithDefaultTimeout(timeout time.Duration) JetStreamOpt {
	return func(options *Options) { options.DefaultTimeout = timeout }
}

type AckPolicy int

const AckExplicitPolicy AckPolicy = 1

type ConsumerConfig struct {
	Durable       string
	Description   string
	DeliverPolicy DeliverPolicy
	FilterSubject string
	AckPolicy     AckPolicy
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
}

type StreamConfig struct {
	Name              string
	Description       string
	Subjects          []string
	Retention         RetentionPolicy
	MaxConsumers      int
	MaxMsgs           int64
	MaxMsgsPerSubject int64
	MaxBytes          int64
	Discard           DiscardPolicy
	MaxAge            time.Duration
	MaxMsgSize        int32
	Storage           StorageType
	Replicas          int
	Duplicates        time.Duration
}

type RetentionPolicy int

const WorkQueuePolicy RetentionPolicy = 1

type DiscardPolicy int

const DiscardNewPolicy DiscardPolicy = 1

type StorageType int

const FileStorage StorageType = 1

type DeliverPolicy int

const DeliverAllPolicy DeliverPolicy = 1

type Stream interface{}

type PubAck struct {
	Stream   string
	Sequence uint64
}

type PublishOpt interface{}

func WithMsgID(string) PublishOpt        { return nil }
func WithExpectStream(string) PublishOpt { return nil }

type ConsumerInfo struct{ Config ConsumerConfig }

type JetStream interface {
	Consumer(context.Context, string, string) (Consumer, error)
}

type Consumer interface {
	Info(context.Context) (*ConsumerInfo, error)
	Messages(...PullMessagesOpt) (MessagesContext, error)
}

type Msg interface {
	Data() []byte
	DoubleAck(context.Context) error
	NakWithDelay(time.Duration) error
	Term() error
}

type MessagesContext interface {
	Next(...NextOpt) (Msg, error)
	Stop()
}

type PullMessagesOpt interface{}
type NextOpt interface{}
type nextContext struct{ ctx context.Context }

func NextContext(ctx context.Context) NextOpt { return nextContext{ctx: ctx} }
func PullMaxMessages(int) PullMessagesOpt     { return nil }

func New(_ *nats.Conn, opts ...JetStreamOpt) (JetStream, error) {
	options := &Options{}
	for _, opt := range opts {
		opt(options)
	}
	return unavailableJetStream{}, nil
}

type unavailableJetStream struct{}

func (unavailableJetStream) Consumer(context.Context, string, string) (Consumer, error) {
	return nil, errors.New("test JetStream consumer is not configured")
}
