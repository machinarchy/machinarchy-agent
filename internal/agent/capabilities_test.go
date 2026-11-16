package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	servicesv1 "github.com/machinarchy/machinarchy-protocol/services/v1"
)

func TestDefaultCapabilityRegistryContainsOnlyReadOnlyBaseline(t *testing.T) {
	registry, err := defaultCapabilityRegistry("test", validTestConfig())
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry() error = %v", err)
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
	config := validTestConfig()
	config.ServiceManagementEnabled = true
	config.AllowedServices = []string{"nginx.service"}
	config.SystemctlPath = systemctlPath
	registry, err := defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry(status) error = %v", err)
	}
	if got, want := strings.Join(registry.Names(), ","), "runtime.info,service.status,system.facts"; got != want {
		t.Fatalf("status capabilities = %q, want %q", got, want)
	}

	config.ServiceControlEnabled = true
	registry, err = defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry(control) error = %v", err)
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
	config := validTestConfig()
	config.ServiceManagementEnabled = true
	config.ServiceControlEnabled = true
	config.AllowedServices = []string{"nginx.service"}
	config.SystemctlPath = systemctlPath
	registry, err := defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.lookup("runtime.info")
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
