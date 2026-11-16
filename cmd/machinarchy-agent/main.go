// Package main is the entry point for the Machinarchy agent application.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/machinarchy/machinarchy-agent/internal/agent"
)

// Release metadata is populated by release builds through -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Machinarchy agent stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print version information and exit")
	checkConfig := flag.Bool("check-config", false, "validate Agent configuration and exit")
	flag.Parse()
	if *showVersion && *checkConfig {
		return fmt.Errorf("--version and --check-config cannot be used together")
	}
	if *showVersion {
		fmt.Printf("machinarchy-agent %s (commit=%s build_time=%s)\n", Version, Commit, BuildTime)
		return nil
	}

	cfg, err := agent.LoadConfig()
	if err != nil {
		return err
	}
	if *checkConfig {
		fmt.Println("Machinarchy Agent configuration is valid.")
		return nil
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(os.Getenv("MACHINARCHY_AGENT_VERSION")) == "" && Version != "" {
		cfg.AgentVersion = Version
	}
	identityStore, err := agent.NewFileIdentityStore(cfg.IdentityFile)
	if err != nil {
		return err
	}
	session, err := agent.NewAgentSession(cfg, logger, identityStore)
	if err != nil {
		return err
	}
	runtime, err := agent.NewRuntime(cfg, logger, agent.NewNATSConnector(logger), session)
	if err != nil {
		return err
	}

	if err := runtime.Run(ctx); err != nil {
		return err
	}
	return nil
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
