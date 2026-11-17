package capabilities

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// fakeSystemctl writes a shell script standing in for systemctl. The service
// adapter is plain os/exec, so these tests run on any Unix, not only Linux.
func fakeSystemctl(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

const showOutput = `printf '%s\n' 'Id=nginx.service' 'LoadState=loaded' 'ActiveState=active' 'SubState=running' 'UnitFileState=enabled' 'Description=nginx'`

func TestRegistryBaselineIsReadOnly(t *testing.T) {
	registry, err := New(Options{AgentVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{agentv1.CapabilityRuntimeInfo, agentv1.CapabilitySystemFacts}
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRegistryAddsServiceCapabilitiesOnlyWhenEnabled(t *testing.T) {
	path := fakeSystemctl(t, "exit 0")
	statusOnly, err := New(Options{ServiceEnabled: true, Services: ServiceOptions{SystemctlPath: path, Allowed: []string{"nginx.service"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := statusOnly.Lookup(agentv1.CapabilityServiceControl); ok {
		t.Fatal("service.control registered without ControlEnabled")
	}
	full, err := New(Options{ServiceEnabled: true, Services: ServiceOptions{SystemctlPath: path, Allowed: []string{"nginx.service"}, ControlEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := full.Lookup(agentv1.CapabilityServiceControl); !ok {
		t.Fatal("service.control missing when enabled")
	}
	output, err := mustLookup(t, full, agentv1.CapabilityRuntimeInfo).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var info agentv1.RuntimeInfo
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Capabilities, full.Names()) {
		t.Fatalf("runtime.info capabilities = %v, want %v", info.Capabilities, full.Names())
	}
}

func TestRegistryRejectsUnsafeServiceConfig(t *testing.T) {
	path := fakeSystemctl(t, "exit 0")
	for name, options := range map[string]ServiceOptions{
		"empty allowlist":  {SystemctlPath: path},
		"bad unit":         {SystemctlPath: path, Allowed: []string{"-x.service"}},
		"relative binary":  {SystemctlPath: "systemctl", Allowed: []string{"nginx.service"}},
		"missing binary":   {SystemctlPath: "/nonexistent/systemctl", Allowed: []string{"nginx.service"}},
		"non-service unit": {SystemctlPath: path, Allowed: []string{"nginx.socket"}},
	} {
		if _, err := New(Options{ServiceEnabled: true, Services: options}); err == nil {
			t.Errorf("%s: New() succeeded", name)
		}
	}
}

func mustLookup(t *testing.T, registry *Registry, name string) Capability {
	t.Helper()
	capability, ok := registry.Lookup(name)
	if !ok {
		t.Fatalf("capability %s not registered", name)
	}
	return capability
}

func TestEmptyInputCapabilitiesRejectFields(t *testing.T) {
	registry, err := New(Options{AgentVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{agentv1.CapabilityRuntimeInfo, agentv1.CapabilitySystemFacts} {
		for _, input := range []string{`{"x":1}`, `[]`, `{} {}`} {
			_, err := mustLookup(t, registry, name).Execute(context.Background(), json.RawMessage(input))
			if !IsInvalidInput(err) {
				t.Errorf("%s(%s) error = %v, want invalid input", name, input, err)
			}
		}
	}
}

func TestSystemFactsAreBoundedAndNonSensitive(t *testing.T) {
	t.Setenv("MACHINARCHY_AGENT_NATS_TOKEN", "secret-token-value")
	facts, err := CollectSystemFacts(context.Background(), "1.0.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(facts)
	if len(encoded) > maxFactsBytes || strings.Contains(string(encoded), "secret-token-value") {
		t.Fatalf("facts too large or leak environment: %s", encoded)
	}
	if facts.OS.Platform == "" || facts.LogicalCPUs < 1 {
		t.Fatalf("baseline facts missing: %+v", facts)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectSystemFacts(cancelled, "1", time.Now()); err == nil {
		t.Fatal("cancelled context ignored")
	}
}

func TestFactParsers(t *testing.T) {
	name, version := parseOSRelease([]byte("NAME=Debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"\n"))
	if name != "Debian GNU/Linux 12 (bookworm)" || version != "12" {
		t.Fatalf("parseOSRelease() = %q, %q", name, version)
	}
	memory := parseMeminfo([]byte("MemTotal:  2048 kB\nMemFree: 1 kB\nMemAvailable: 1024 kB\n"))
	if memory == nil || memory.TotalBytes != 2048*1024 || memory.AvailableBytes != 1024*1024 {
		t.Fatalf("parseMeminfo() = %+v", memory)
	}
	if parseMeminfo([]byte("MemTotal: 1 kB\nMemAvailable: 2 kB\n")) != nil {
		t.Fatal("available > total accepted")
	}
	if uptime := parseUptime([]byte("1234.56 99.0\n")); uptime == nil || *uptime != 1234 {
		t.Fatalf("parseUptime() = %v", uptime)
	}
	for _, bad := range []string{"", "NaN 1", "-1 0", "x"} {
		if parseUptime([]byte(bad)) != nil {
			t.Errorf("parseUptime(%q) accepted", bad)
		}
	}
	if got := sanitize("a\nb\x00c ", 10); got != "a b c" {
		t.Fatalf("sanitize() = %q", got)
	}
}

func TestServiceStatusParsesAndValidates(t *testing.T) {
	path := fakeSystemctl(t, showOutput)
	registry, err := New(Options{ServiceEnabled: true, Services: ServiceOptions{SystemctlPath: path, Allowed: []string{"nginx.service"}}})
	if err != nil {
		t.Fatal(err)
	}
	status := mustLookup(t, registry, agentv1.CapabilityServiceStatus)
	output, err := status.Execute(context.Background(), json.RawMessage(`{"service":"nginx.service"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got agentv1.ServiceStatus
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	want := agentv1.ServiceStatus{Service: "nginx.service", LoadState: "loaded", ActiveState: "active", SubState: "running", UnitFileState: "enabled", Description: "nginx"}
	if got != want {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
	for _, input := range []string{`{"service":"ssh.service"}`, `{"service":"--help"}`, `{"service":"nginx.service","x":1}`} {
		if _, err := status.Execute(context.Background(), json.RawMessage(input)); !IsInvalidInput(err) {
			t.Errorf("status(%s) error = %v, want invalid input", input, err)
		}
	}
}

func TestServiceControlUsesFixedArgumentLayout(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "args")
	path := fakeSystemctl(t, `printf '%s\n' "$@" > "`+logPath+`"; env > "`+logPath+`.env"`)
	t.Setenv("MACHINARCHY_AGENT_NATS_TOKEN", "must-not-leak")
	registry, err := New(Options{ServiceEnabled: true, Services: ServiceOptions{SystemctlPath: path, Allowed: []string{"nginx.service"}, ControlEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	control := mustLookup(t, registry, agentv1.CapabilityServiceControl)
	if _, err := control.Execute(context.Background(), json.RawMessage(`{"service":"nginx.service","action":"restart"}`)); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(logPath)
	if got := strings.Fields(string(args)); !reflect.DeepEqual(got, []string{"--no-ask-password", "--", "restart", "nginx.service"}) {
		t.Fatalf("systemctl args = %v", got)
	}
	env, _ := os.ReadFile(logPath + ".env")
	if strings.Contains(string(env), "must-not-leak") {
		t.Fatal("agent environment leaked to systemctl")
	}
	for _, input := range []string{`{"service":"nginx.service","action":"mask"}`, `{"service":"nginx.service","action":"--now"}`, `{"service":"other.service","action":"stop"}`} {
		if _, err := control.Execute(context.Background(), json.RawMessage(input)); !IsInvalidInput(err) {
			t.Errorf("control(%s) error = %v, want invalid input", input, err)
		}
	}
}

func TestServiceControlSerializesPerUnit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	path := fakeSystemctl(t, `echo start >> "`+logPath+`"; sleep 0.2; echo end >> "`+logPath+`"`)
	s := &systemctl{path: path, locks: map[string]*sync.Mutex{}}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Control(context.Background(), "nginx.service", "restart"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(logPath)
	if got := strings.Fields(string(data)); !reflect.DeepEqual(got, []string{"start", "end", "start", "end", "start", "end"}) {
		t.Fatalf("actions on one unit overlapped: %v", got)
	}
	first, again, other := s.unitLock("a.service"), s.unitLock("a.service"), s.unitLock("b.service")
	if first != again || first == other {
		t.Fatal("unit locks are not stable per unit")
	}
}

func TestSystemctlFailuresAreBounded(t *testing.T) {
	path := fakeSystemctl(t, `head -c 20000 /dev/zero | tr '\0' 'x'`)
	s := &systemctl{path: path, locks: map[string]*sync.Mutex{}}
	if _, err := s.Status(context.Background(), "nginx.service"); err == nil {
		t.Fatal("oversized output accepted")
	}
	exitPath := fakeSystemctl(t, "exit 5")
	s = &systemctl{path: exitPath, locks: map[string]*sync.Mutex{}}
	if err := s.Control(context.Background(), "nginx.service", "stop"); err == nil || !strings.Contains(err.Error(), "status 5") {
		t.Fatalf("Control() error = %v", err)
	}
	slowPath := fakeSystemctl(t, "sleep 5")
	s = &systemctl{path: slowPath, locks: map[string]*sync.Mutex{}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := s.Status(ctx, "nginx.service"); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, bad := range []string{"Id=other.service\nLoadState=x\nActiveState=x\nSubState=x", "garbage", "Id=nginx.service\nId=nginx.service", "Id=nginx.service\nEvil=1"} {
		if _, err := parseShow("nginx.service", bad); err == nil {
			t.Errorf("parseShow(%q) accepted", bad)
		}
	}
}

func TestSystemctlEnvironment(t *testing.T) {
	env := systemctlEnv("unix:path=/run/dbus/system_bus_socket")
	if env[len(env)-1] != "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket" {
		t.Fatalf("bus address not passed: %v", env)
	}
	for _, bad := range []string{"", "tcp:host=evil,port=1", "unix:path=/x;unix:path=/y", "unix:abstract=x"} {
		for _, entry := range systemctlEnv(bad) {
			if strings.HasPrefix(entry, "DBUS_SYSTEM_BUS_ADDRESS=") {
				t.Errorf("bus address %q passed through", bad)
			}
		}
	}
}
