// Command machinarchy-agent is the Machinarchy host agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/app"
	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Set by release builds through -ldflags "-X main.Version=...".
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("machinarchy-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print version and exit")
	checkConfig := flags.Bool("check-config", false, "validate configuration and exit")
	showKey := flags.Bool("public-key", false, "print the agent's public identity key (creating state if needed) and exit")
	healthcheck := flags.Bool("healthcheck", false, "exit 0 if the agent is enrolled and holds an unexpired lease (container HEALTHCHECK)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(stdout, "machinarchy-agent %s (commit %s, built %s, protocol v%d)\n", Version, Commit, BuildTime, agentv1.ProtocolVersion)
		return 0
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 2
	}
	if *healthcheck {
		if err := state.Health(cfg.StateFile, time.Now()); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if *checkConfig {
		_, _ = fmt.Fprintln(stdout, "configuration is valid")
		return 0
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("service", "machinarchy-agent", "version", Version)
	slog.SetDefault(logger)

	agent, err := app.New(cfg, Version, logger)
	if err != nil {
		logger.Error("agent failed to start", "error", err)
		return 1
	}
	if *showKey {
		_, _ = fmt.Fprintln(stdout, agentv1.EncodePublicKey(agent.PublicKey()))
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("agent starting", "state_file", cfg.StateFile, "environment", cfg.Environment)
	if err := agent.Run(ctx); err != nil {
		logger.Error("agent stopped with an error", "error", err)
		return 1
	}
	logger.Info("agent stopped")
	return 0
}
