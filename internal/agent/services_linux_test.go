//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	servicesv1 "github.com/machinarchy/machinarchy-protocol/services/v1"
)

func writeFakeSystemctl(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceStatusCapabilityReturnsNormalizedState(t *testing.T) {
	path := writeFakeSystemctl(t, `
case "$1" in
  show) printf '%s\n' 'Id=nginx.service' 'LoadState=loaded' 'ActiveState=active' 'SubState=running' 'UnitFileState=enabled' 'Description=The nginx HTTP server' ;;
  *) exit 9 ;;
esac`)
	manager := &systemdServiceManager{path: path}
	capability := serviceStatusCapability(manager, map[string]struct{}{"nginx.service": {}})
	input, _ := json.Marshal(servicesv1.StatusRequest{Service: "nginx.service"})

	output, err := capability.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var status servicesv1.StatusResponse
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Service != "nginx.service" || status.LoadState != "loaded" || status.ActiveState != "active" || status.SubState != "running" || status.UnitFileState != "enabled" || status.Description != "The nginx HTTP server" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestServiceStatusRejectsUnlistedAndMalformedRequests(t *testing.T) {
	path := writeFakeSystemctl(t, "exit 0")
	capability := serviceStatusCapability(&systemdServiceManager{path: path}, map[string]struct{}{"nginx.service": {}})
	for _, input := range []string{
		`{"service":"ssh.service"}`,
		`{"service":"-bad.service"}`,
		`{"service":"nginx.service","extra":true}`,
		`{"service":"nginx.service"} {}`,
	} {
		if _, err := capability.Execute(context.Background(), []byte(input)); err == nil {
			t.Errorf("Execute(%s) succeeded", input)
		}
	}
}

func TestServiceManagerDoesNotInheritAgentSecrets(t *testing.T) {
	t.Setenv("MACHINARCHY_AGENT_NATS_TOKEN", "must-not-leak")
	path := writeFakeSystemctl(t, `if env | grep -q '^MACHINARCHY_AGENT_NATS_TOKEN='; then exit 88; fi
printf '%s\n' 'Id=nginx.service' 'LoadState=loaded' 'ActiveState=active' 'SubState=running'`)
	manager := &systemdServiceManager{path: path}
	if _, err := manager.Status(context.Background(), "nginx.service"); err != nil {
		t.Fatalf("Status() leaked parent environment or failed: %v", err)
	}
}

func TestServiceControlExecutesOnlyAllowlistedFixedAction(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "arguments.log")
	path := writeFakeSystemctl(t, `printf '%s\n' "$@" > "`+logPath+`"`)
	capability := serviceControlCapability(&systemdServiceManager{path: path}, map[string]struct{}{"nginx.service": {}})
	input, _ := json.Marshal(servicesv1.ControlRequest{Service: "nginx.service", Action: servicesv1.ActionRestart})

	output, err := capability.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var response servicesv1.ControlResponse
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Result != "completed" || response.Action != "restart" || response.Service != "nginx.service" {
		t.Fatalf("unexpected response: %#v", response)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "restart\nnginx.service" {
		t.Fatalf("systemctl arguments = %q; controller input must not add flags or arguments", got)
	}
}

func TestServiceControlRejectsUnsupportedActionAndUnlistedService(t *testing.T) {
	path := writeFakeSystemctl(t, "exit 0")
	capability := serviceControlCapability(&systemdServiceManager{path: path}, map[string]struct{}{"nginx.service": {}})
	for _, input := range []string{
		`{"service":"nginx.service","action":"enable"}`,
		`{"service":"nginx.service","action":"daemon-reload"}`,
		`{"service":"ssh.service","action":"restart"}`,
		`{"service":"nginx.service","action":"restart","args":["--no-block"]}`,
	} {
		if _, err := capability.Execute(context.Background(), []byte(input)); err == nil {
			t.Errorf("Execute(%s) succeeded", input)
		}
	}
}

func TestServiceControlSerializesMutationsPerUnit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "mutations.log")
	path := writeFakeSystemctl(t, `printf 'begin:%s\n' "$1" >> "`+logPath+`"
sleep 0.1
printf 'end:%s\n' "$1" >> "`+logPath+`"`)
	manager := &systemdServiceManager{path: path}
	capability := serviceControlCapability(manager, map[string]struct{}{"nginx.service": {}})
	var wg sync.WaitGroup
	for _, action := range []string{servicesv1.ActionRestart, servicesv1.ActionStop} {
		action := action
		wg.Add(1)
		go func() {
			defer wg.Done()
			input, _ := json.Marshal(servicesv1.ControlRequest{Service: "nginx.service", Action: action})
			if _, err := capability.Execute(context.Background(), input); err != nil {
				t.Errorf("Execute(%s) error = %v", action, err)
			}
		}()
	}
	wg.Wait()
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "begin:") || !strings.HasPrefix(lines[1], "end:") || !strings.HasPrefix(lines[2], "begin:") || !strings.HasPrefix(lines[3], "end:") {
		t.Fatalf("service mutations overlapped: %q", contents)
	}
}

func TestServiceStatusHonorsContextCancellation(t *testing.T) {
	path := writeFakeSystemctl(t, "sleep 5\nexit 0")
	manager := &systemdServiceManager{path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := manager.Status(ctx, "nginx.service")
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Status() error = %v, context error = %v; want cancellation", err, ctx.Err())
	}
}

func TestParseSystemdStatusRejectsMalformedOutput(t *testing.T) {
	cases := []string{
		"LoadState=loaded\nActiveState=active\nSubState=running\n",
		"Id=ssh.service\nLoadState=loaded\nActiveState=active\nSubState=running\n",
		"Id=nginx.service\nLoadState=loaded\nActiveState=active\nSubState=running\nUnexpected=bad\n",
		"Id=nginx.service\nLoadState=loaded\nLoadState=loaded\nActiveState=active\nSubState=running\n",
	}
	for _, output := range cases {
		if _, err := parseSystemdStatus("nginx.service", output); err == nil {
			t.Errorf("parseSystemdStatus(%q) succeeded", output)
		}
	}
}

func TestDefaultCapabilityRegistryAddsServicesOnlyWhenConfigured(t *testing.T) {
	path := writeFakeSystemctl(t, "exit 0")
	config := validTestConfig()
	config.ServiceManagementEnabled = true
	config.AllowedServices = []string{"nginx.service"}
	config.SystemctlPath = path

	registry, err := defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry(status) error = %v", err)
	}
	if got := strings.Join(registry.Names(), ","); got != "runtime.info,service.status,system.facts" {
		t.Fatalf("status-only capabilities = %q", got)
	}

	config.ServiceControlEnabled = true
	registry, err = defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry(control) error = %v", err)
	}
	if got := strings.Join(registry.Names(), ","); got != "runtime.info,service.control,service.status,system.facts" {
		t.Fatalf("service control capabilities = %q", got)
	}
	info, ok := registry.lookup("runtime.info")
	if !ok {
		t.Fatal("runtime.info capability missing")
	}
	output, err := info.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var runtimeInfo struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(output, &runtimeInfo); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runtimeInfo.Capabilities, ","), "service.control") {
		t.Fatalf("runtime.info capabilities = %#v", runtimeInfo.Capabilities)
	}
}
