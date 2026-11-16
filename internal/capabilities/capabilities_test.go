package capabilities

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"

	servicesv1 "github.com/machinarchy/machinarchy-protocol/services/v1"
)

func capabilityTestConfig() config.Config {
	return config.Config{
		Environment: "test", LogLevel: "info", ClientName: "machinarchy-agent-test",
		BrokerURLs: "nats://127.0.0.1:4222", ConnectTimeout: time.Second,
		StartupTimeout: time.Second, ReconnectWait: time.Second, DrainTimeout: time.Second,
		RequestTimeout: time.Second, EventBufferSize: config.DefaultEventBufferSize,
		OperationWorkers: config.DefaultOperationWorkers, OperationQueueSize: config.DefaultOperationQueue,
		MaxOperationTimeout: 5 * time.Minute, MaxOperationInputBytes: config.MaxOperationInputBytes,
		MaxOperationOutputBytes: config.MaxOperationOutputBytes, ResultPublishTimeout: 5 * time.Second,
		IdentityFile: "./data/agent/identity.json", AgentVersion: "test",
		RegistrationSubject: "machinarchy.v1.agent.register", LeaseRenewSubject: "machinarchy.v1.agent.lease.renew",
	}
}

func TestDefaultCapabilityRegistryContainsOnlyReadOnlyBaseline(t *testing.T) {
	registry, err := NewDefaultRegistry("test", capabilityTestConfig())
	if err != nil {
		t.Fatalf("NewDefaultRegistry() error = %v", err)
	}
	if got, want := strings.Join(registry.Names(), ","), "runtime.info,system.facts"; got != want {
		t.Fatalf("default capabilities = %q, want %q", got, want)
	}
}

func TestCapabilityRegistryAddsOnlyExplicitlyEnabledServiceOperations(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the initial service provider is Linux/systemd only")
	}
	systemctlPath := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(systemctlPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := capabilityTestConfig()
	cfg.ServiceManagementEnabled = true
	cfg.AllowedServices = []string{"nginx.service"}
	cfg.SystemctlPath = systemctlPath
	registry, err := NewDefaultRegistry("test", cfg)
	if err != nil {
		t.Fatalf("NewDefaultRegistry(status) error = %v", err)
	}
	if got, want := strings.Join(registry.Names(), ","), "runtime.info,service.status,system.facts"; got != want {
		t.Fatalf("status capabilities = %q, want %q", got, want)
	}

	cfg.ServiceControlEnabled = true
	registry, err = NewDefaultRegistry("test", cfg)
	if err != nil {
		t.Fatalf("NewDefaultRegistry(control) error = %v", err)
	}
	if got, want := strings.Join(registry.Names(), ","), "runtime.info,service.control,service.status,system.facts"; got != want {
		t.Fatalf("service capabilities = %q, want %q", got, want)
	}
}

func TestRuntimeInfoReportsOnlyRegisteredCapabilities(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the initial service provider is Linux/systemd only")
	}
	systemctlPath := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(systemctlPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := capabilityTestConfig()
	cfg.ServiceManagementEnabled = true
	cfg.ServiceControlEnabled = true
	cfg.AllowedServices = []string{"nginx.service"}
	cfg.SystemctlPath = systemctlPath
	registry, err := NewDefaultRegistry("test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.Lookup("runtime.info")
	if !ok {
		t.Fatal("runtime.info capability is missing")
	}
	output, err := capability.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"runtime.info", servicesv1.CapabilityControl, servicesv1.CapabilityStatus, "system.facts"}
	if len(result.Capabilities) != len(want) {
		t.Fatalf("runtime.info capabilities = %#v, want %#v", result.Capabilities, want)
	}
	for i := range want {
		if result.Capabilities[i] != want[i] {
			t.Fatalf("runtime.info capabilities = %#v, want %#v", result.Capabilities, want)
		}
	}
}
