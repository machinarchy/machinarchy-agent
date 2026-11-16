package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	agenttransport "github.com/machinarchy/machinarchy-agent/internal/transport"
)

// Runtime owns the agent process's broker connection lifecycle.
type Runtime struct {
	config    config.Config
	logger    *slog.Logger
	connector agenttransport.Connector
	session   Session
}

// NewRuntime accepts an optional agent session. Keeping it optional preserves
// the Phase 1 connection-only behavior for transport tests and staged rollout.
func NewRuntime(cfg config.Config, logger *slog.Logger, connector agenttransport.Connector, sessions ...Session) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate agent configuration: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if connector == nil {
		return nil, errors.New("agent broker connector is required")
	}
	if len(sessions) > 1 {
		return nil, errors.New("at most one agent session may be configured")
	}
	var session Session
	if len(sessions) == 1 {
		session = sessions[0]
	}
	return &Runtime{
		config:    cfg,
		logger:    logger.With("component", "agent"),
		connector: connector,
		session:   session,
	}, nil
}

// Run connects to the event broker, waits for the first successful
// connection, monitors connection lifecycle events, and drains on shutdown.
// NATS handles reconnect attempts; this runtime observes and reports them.
func (r *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("agent runtime context is required")
	}
	if ctx.Err() != nil {
		return nil
	}

	events := make(chan agenttransport.ConnectionEvent, 32)
	conn, err := r.connector.Connect(r.config, events)
	if err != nil {
		return fmt.Errorf("connect agent to event broker: %w", err)
	}
	if conn == nil {
		return errors.New("agent broker connector returned a nil connection")
	}
	defer conn.Close()

	if err := r.waitForInitialConnection(ctx, conn, events); err != nil {
		shutdownErr := r.shutdown(conn)
		if ctx.Err() != nil {
			return shutdownErr
		}
		return err
	}

	r.logger.Info("agent connected to event broker", "transport", "nats")
	if r.session != nil {
		sessionErr := r.session.Run(ctx, conn, events)
		shutdownErr := r.shutdown(conn)
		if sessionErr != nil && ctx.Err() == nil {
			if shutdownErr != nil {
				return errors.Join(sessionErr, shutdownErr)
			}
			return sessionErr
		}
		return shutdownErr
	}
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("agent shutdown requested")
			return r.shutdown(conn)
		case event := <-events:
			switch event.Type {
			case agenttransport.EventDisconnected:
				if event.Err != nil {
					r.logger.Warn("agent disconnected from event broker; reconnecting", "error", event.Err)
				} else {
					r.logger.Warn("agent disconnected from event broker; reconnecting")
				}
			case agenttransport.EventReconnected:
				r.logger.Info("agent reconnected to event broker")
			case agenttransport.EventAsyncError:
				if event.Err != nil {
					r.logger.Warn("asynchronous event broker error", "error", event.Err)
				}
			case agenttransport.EventClosed:
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("event broker connection closed unexpectedly")
			default:
				r.logger.Warn("unknown event broker lifecycle event", "event_type", event.Type)
			}
		}
	}
}

func (r *Runtime) waitForInitialConnection(ctx context.Context, conn agenttransport.Connection, events <-chan agenttransport.ConnectionEvent) error {
	timer := time.NewTimer(r.config.StartupTimeout)
	defer timer.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if conn.IsConnected() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for initial event broker connection: %w", ctx.Err())
		case <-timer.C:
			return fmt.Errorf("event broker connection was not established within %s", r.config.StartupTimeout)
		case event := <-events:
			switch event.Type {
			case agenttransport.EventDisconnected:
				if event.Err != nil {
					r.logger.Warn("initial event broker connection unavailable; retrying", "error", event.Err)
				}
			case agenttransport.EventAsyncError:
				if event.Err != nil {
					r.logger.Warn("asynchronous event broker error during startup", "error", event.Err)
				}
			case agenttransport.EventClosed:
				return errors.New("event broker connection closed before startup completed")
			case agenttransport.EventReconnected:
				// The next loop iteration checks the connection state.
			}
		case <-ticker.C:
		}
	}
}

func (r *Runtime) shutdown(conn agenttransport.Connection) error {
	// The concrete NATS connection is configured with DrainTimeout, keeping
	// this operation bounded without introducing an untracked goroutine.
	if err := conn.Drain(); err != nil {
		r.logger.Warn("event broker drain failed; forcing connection close", "error", err)
		conn.Close()
		return fmt.Errorf("drain event broker connection: %w", err)
	}
	r.logger.Info("event broker connection drained")
	return nil
}
