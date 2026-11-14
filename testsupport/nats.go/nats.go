package nats

import (
	"context"
	"errors"
	"time"
)

type Option func(*Conn)
type Msg struct{ Data []byte }
type Conn struct {
	connected bool
	closed    bool
}
type Subscription struct{}

func Name(string) Option                                    { return func(*Conn) {} }
func Timeout(time.Duration) Option                          { return func(*Conn) {} }
func MaxReconnects(int) Option                              { return func(*Conn) {} }
func ReconnectWait(time.Duration) Option                    { return func(*Conn) {} }
func ReconnectJitter(time.Duration, time.Duration) Option   { return func(*Conn) {} }
func RetryOnFailedConnect(bool) Option                      { return func(*Conn) {} }
func DrainTimeout(time.Duration) Option                     { return func(*Conn) {} }
func PingInterval(time.Duration) Option                     { return func(*Conn) {} }
func MaxPingsOutstanding(int) Option                        { return func(*Conn) {} }
func DisconnectErrHandler(func(*Conn, error)) Option        { return func(*Conn) {} }
func ReconnectHandler(func(*Conn)) Option                   { return func(*Conn) {} }
func ClosedHandler(func(*Conn)) Option                      { return func(*Conn) {} }
func ErrorHandler(func(*Conn, *Subscription, error)) Option { return func(*Conn) {} }
func UserCredentials(string) Option                         { return func(*Conn) {} }
func Token(string) Option                                   { return func(*Conn) {} }
func UserInfo(string, string) Option                        { return func(*Conn) {} }

func Connect(_ string, options ...Option) (*Conn, error) {
	c := &Conn{connected: true}
	for _, option := range options {
		option(c)
	}
	return c, nil
}
func (c *Conn) IsConnected() bool            { return c != nil && c.connected && !c.closed }
func (c *Conn) IsClosed() bool               { return c == nil || c.closed }
func (c *Conn) Publish(string, []byte) error { return nil }
func (c *Conn) RequestWithContext(context.Context, string, []byte) (*Msg, error) {
	return nil, errors.New("stub")
}
func (c *Conn) Subscribe(string, func(*Msg)) (*Subscription, error)    { return &Subscription{}, nil }
func (c *Conn) ChanSubscribe(string, chan *Msg) (*Subscription, error) { return &Subscription{}, nil }
func (c *Conn) FlushTimeout(time.Duration) error                       { return nil }
func (c *Conn) Drain() error {
	if c != nil {
		c.closed = true
	}
	return nil
}
func (c *Conn) Close() {
	if c != nil {
		c.closed = true
	}
}
func (s *Subscription) SetPendingLimits(int, int) error { return nil }
func (s *Subscription) Unsubscribe() error              { return nil }
