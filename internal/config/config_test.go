package config

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

var controllerKey = agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey))

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func devEnv(extra map[string]string) func(string) string {
	values := map[string]string{
		EnvEnvironment:         Development,
		EnvNATSURL:             "nats://127.0.0.1:4222",
		EnvControllerPublicKey: controllerKey,
	}
	for k, v := range extra {
		values[k] = v
	}
	return env(values)
}

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(devEnv(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Workers != 4 || cfg.QueueSize != 32 || cfg.MaxOutputBytes != DefaultOutputSize ||
		cfg.MaxOperationTimeout != 5*time.Minute || cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.StateFile) || !strings.HasSuffix(cfg.StateFile, filepath.Join("data", "agent", "state.json")) {
		t.Fatalf("StateFile = %q", cfg.StateFile)
	}
	if cfg.ServiceEnabled {
		t.Fatal("service management enabled by default")
	}
}

func TestDefaultEnvironmentIsProductionAndEnforcesTLSAndAuth(t *testing.T) {
	base := map[string]string{EnvNATSURL: "nats://broker:4222", EnvControllerPublicKey: controllerKey}
	if _, err := Load(env(base)); err == nil || !strings.Contains(err.Error(), "tls://") {
		t.Fatalf("plaintext NATS accepted by default environment: %v", err)
	}
	base[EnvNATSURL] = "tls://broker:4222"
	if _, err := Load(env(base)); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("unauthenticated production config accepted: %v", err)
	}
	base[EnvNATSCredentialsFile] = writeFile(t, "creds", 0o600)
	cfg, err := Load(env(base))
	if err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
	if cfg.Environment != Production || cfg.StateFile != "/var/lib/machinarchy-agent/state.json" {
		t.Fatalf("unexpected production defaults: %+v", cfg)
	}
}

func TestControllerKeyIsRequired(t *testing.T) {
	values := map[string]string{EnvEnvironment: Development, EnvNATSURL: "nats://h:1"}
	if _, err := Load(env(values)); err == nil {
		t.Fatal("missing controller key accepted")
	}
	values[EnvControllerPublicKey] = "not-a-key"
	if _, err := Load(env(values)); err == nil {
		t.Fatal("malformed controller key accepted")
	}
	values[EnvControllerPublicKey] = ""
	values[EnvControllerPublicKeyFile] = writeFile(t, controllerKey+"\n", 0o644)
	if _, err := Load(env(values)); err != nil {
		t.Fatalf("controller key file rejected: %v", err)
	}
}

func TestEnrollmentTokenFromFileMustBePrivate(t *testing.T) {
	token := strings.Repeat("e", 40)
	if _, err := Load(devEnv(map[string]string{EnvEnrollmentTokenFile: writeFile(t, token, 0o644)})); err == nil {
		t.Fatal("world-readable enrollment token file accepted")
	}
	cfg, err := Load(devEnv(map[string]string{EnvEnrollmentTokenFile: writeFile(t, token+"\n", 0o600)}))
	if err != nil || cfg.EnrollmentToken != token {
		t.Fatalf("Load() = %q, %v", cfg.EnrollmentToken, err)
	}
	if _, err := Load(devEnv(map[string]string{EnvEnrollmentToken: "short"})); err == nil {
		t.Fatal("short enrollment token accepted")
	}
	if _, err := Load(devEnv(map[string]string{EnvEnrollmentToken: token, EnvEnrollmentTokenFile: "/x"})); err == nil {
		t.Fatal("both token value and file accepted")
	}
}

func TestRejectsInvalidSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"removed setting":       {"MACHINARCHY_AGENT_JETSTREAM_STREAM": "X"},
		"bad environment":       {EnvEnvironment: "staging"},
		"bad url scheme":        {EnvNATSURL: "http://h:1"},
		"url with credentials":  {EnvNATSURL: "nats://user:pw@h:1"},
		"two auth methods":      {EnvNATSToken: "t", EnvNATSUsername: "u", EnvNATSPassword: "p"},
		"username only":         {EnvNATSUsername: "u"},
		"cert without key":      {EnvNATSCertFile: "/x"},
		"bad duration":          {EnvRequestTimeout: "forever"},
		"duration too long":     {EnvShutdownTimeout: "1h"},
		"zero workers":          {EnvOperationWorkers: "0"},
		"workers over queue":    {EnvOperationWorkers: "8", EnvOperationQueueSize: "4"},
		"output above protocol": {EnvOperationMaxOutput: "999999999"},
		"control without mgmt":  {EnvServiceControl: "true"},
		"mgmt without list":     {EnvServiceManagement: "true"},
		"list without mgmt":     {EnvServiceAllowlist: "nginx.service"},
		"bad unit":              {EnvServiceManagement: "true", EnvServiceAllowlist: "nginx.socket"},
		"bad log level":         {EnvLogLevel: "trace"},
		"multiline name":        {EnvAgentName: "a\nb"},
	}
	for name, extra := range cases {
		if _, err := Load(devEnv(extra)); err == nil {
			t.Errorf("%s: Load() succeeded", name)
		}
	}
}

func TestPrivateFileChecks(t *testing.T) {
	if _, err := Load(devEnv(map[string]string{EnvNATSCredentialsFile: writeFile(t, "c", 0o644)})); err == nil {
		t.Fatal("world-readable credentials file accepted")
	}
	target := writeFile(t, "c", 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := Load(devEnv(map[string]string{EnvNATSCredentialsFile: link})); err == nil {
		t.Fatal("symlinked credentials file accepted")
	}
}

func TestServiceSettings(t *testing.T) {
	cfg, err := Load(devEnv(map[string]string{
		EnvServiceManagement: "true", EnvServiceControl: "true",
		EnvServiceAllowlist: "nginx.service, ssh.service", EnvSystemctlPath: "/bin/systemctl",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ServiceEnabled || !cfg.Services.ControlEnabled || len(cfg.Services.Allowed) != 2 || cfg.Services.SystemctlPath != "/bin/systemctl" {
		t.Fatalf("service settings = %+v", cfg.Services)
	}
}

// ADR-0004: metrics are off unless a textfile path is set.
func TestMetricsSettings(t *testing.T) {
	cfg, err := Load(devEnv(nil))
	if err != nil || cfg.MetricsFile != "" || cfg.MetricsInterval != 15*time.Second {
		t.Fatalf("defaults: %q %s %v", cfg.MetricsFile, cfg.MetricsInterval, err)
	}
	cfg, err = Load(devEnv(map[string]string{EnvMetricsFile: "/var/lib/node_exporter/textfile_collector/machinarchy_agent.prom", EnvMetricsInterval: "30s"}))
	if err != nil || cfg.MetricsFile == "" || cfg.MetricsInterval != 30*time.Second {
		t.Fatalf("set: %q %s %v", cfg.MetricsFile, cfg.MetricsInterval, err)
	}
	for _, bad := range []map[string]string{
		{EnvMetricsFile: "relative.prom"}, {EnvMetricsFile: "/var/lib/agent.txt"},
		{EnvMetricsInterval: "500ms"}, {EnvMetricsInterval: "1h"},
	} {
		if _, err := Load(devEnv(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// Q-24: the state file path resolves without the rest of the configuration.
func TestStateFilePath(t *testing.T) {
	if got, err := StateFilePath(env(nil)); err != nil || got != "/var/lib/machinarchy-agent/state.json" {
		t.Fatalf("production default = %q, %v", got, err)
	}
	if got, _ := StateFilePath(env(map[string]string{EnvEnvironment: "Development"})); !strings.HasSuffix(got, filepath.Join("data", "agent", "state.json")) || !filepath.IsAbs(got) {
		t.Fatalf("development default = %q", got)
	}
	if got, _ := StateFilePath(env(map[string]string{EnvStateFile: "/srv/agent/state.json"})); got != "/srv/agent/state.json" {
		t.Fatalf("explicit = %q", got)
	}
}

// Phase 16: during a signing-key rotation the agent pins the controller's
// current and next key.
func TestControllerKeysForRotation(t *testing.T) {
	next := agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize)).Public().(ed25519.PublicKey))
	third := agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize)).Public().(ed25519.PublicKey))
	cfg, err := Load(devEnv(map[string]string{EnvControllerPublicKey: controllerKey + ", " + next}))
	if err != nil || len(cfg.ControllerPublicKeys) != 2 {
		t.Fatalf("two keys: %d, %v", len(cfg.ControllerPublicKeys), err)
	}
	file := writeFile(t, controllerKey+"\n"+next+"\n", 0o644) // a public key file need not be private
	cfg, err = Load(devEnv(map[string]string{EnvControllerPublicKey: "", EnvControllerPublicKeyFile: file}))
	if err != nil || len(cfg.ControllerPublicKeys) != 2 {
		t.Fatalf("key file with two lines: %d, %v", len(cfg.ControllerPublicKeys), err)
	}
	for name, value := range map[string]string{
		"three keys": controllerKey + "," + next + "," + third,
		"duplicate":  controllerKey + "," + controllerKey,
		"one bad":    controllerKey + ",not-a-key",
	} {
		if _, err := Load(devEnv(map[string]string{EnvControllerPublicKey: value})); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
