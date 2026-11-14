package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func clearAgentEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		envAgentNATSURL, envEventBrokerURL, envNATSURL, envAgentName, envEnvironment, "MACHINARCHY_LOG_LEVEL",
		envNATSCredentialsFile, envNATSToken, envNATSUsername, envNATSPassword,
		envConnectTimeout, envStartupTimeout, envReconnectWait, envReconnectJitter, envDrainTimeout,
		envAgentStateFile, envAgentEnrollmentToken, envAgentVersion, envRegisterSubject, envLeaseRenewSubject, envRequestTimeout, envEventBufferSize,
		envOperationWorkers, envOperationQueueSize, envOperationTimeout, envOperationInputLimit, envOperationOutputLimit, envResultPublishTimeout,
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv(envAgentNATSURL, "nats://127.0.0.1:4222")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ClientName != "machinarchy-agent" {
		t.Fatalf("ClientName = %q, want %q", cfg.ClientName, "machinarchy-agent")
	}
	if cfg.ConnectTimeout != 5*time.Second || cfg.StartupTimeout != 30*time.Second {
		t.Fatalf("unexpected timeout defaults: connect=%s startup=%s", cfg.ConnectTimeout, cfg.StartupTimeout)
	}
	if cfg.RegistrationSubject != "machinarchy.v1.agent.register" || cfg.LeaseRenewSubject != "machinarchy.v1.agent.lease.renew" {
		t.Fatalf("unexpected control-plane subjects: register=%q renew=%q", cfg.RegistrationSubject, cfg.LeaseRenewSubject)
	}
	if cfg.EventBufferSize != defaultEventBufferSize {
		t.Fatalf("EventBufferSize = %d, want %d", cfg.EventBufferSize, defaultEventBufferSize)
	}
	if cfg.OperationWorkers != defaultOperationWorkers || cfg.OperationQueueSize != defaultOperationQueue {
		t.Fatalf("unexpected operation defaults: workers=%d queue=%d", cfg.OperationWorkers, cfg.OperationQueueSize)
	}
	if cfg.IdentityFile != "./data/agent/identity.json" {
		t.Fatalf("IdentityFile = %q", cfg.IdentityFile)
	}
}

func TestLoadConfigUsesEventBrokerURLFallback(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv(envEventBrokerURL, "tls://broker.example:4222")
	t.Setenv(envEnvironment, "production")
	t.Setenv(envNATSToken, "test-token")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.BrokerURLs != "tls://broker.example:4222" {
		t.Fatalf("BrokerURLs = %q", cfg.BrokerURLs)
	}
}

func TestConfigRejectsPlaintextBrokerInProduction(t *testing.T) {
	cfg := validTestConfig()
	cfg.Environment = "production"
	cfg.BrokerURLs = "nats://broker.example:4222"
	cfg.Token = "token"

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want TLS validation error")
	}
}

func TestConfigRejectsConflictingAuthenticationMethods(t *testing.T) {
	cfg := validTestConfig()
	cfg.Token = "token"
	cfg.Username = "agent"
	cfg.Password = "password"

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want conflicting authentication error")
	}
}

func TestConfigRequiresPrivateCredentialsFile(t *testing.T) {
	cfg := validTestConfig()
	path := filepath.Join(t.TempDir(), "agent.creds")
	if err := os.WriteFile(path, []byte("placeholder credentials"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.CredentialsFile = path
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want permissions error")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with mode 0600 error = %v", err)
	}
}

func TestConfigRejectsInvalidLogLevel(t *testing.T) {
	cfg := validTestConfig()
	cfg.LogLevel = "verbose"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want log level validation error")
	}
}

func TestConfigRejectsURLCredentials(t *testing.T) {
	cfg := validTestConfig()
	cfg.BrokerURLs = "nats://agent:password@broker.example:4222"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want URL userinfo rejection")
	}
}

func validTestConfig() Config {
	return Config{
		Environment:             "test",
		LogLevel:                "info",
		ClientName:              "machinarchy-agent-test",
		BrokerURLs:              "nats://127.0.0.1:4222",
		ConnectTimeout:          time.Second,
		StartupTimeout:          time.Second,
		ReconnectWait:           time.Second,
		DrainTimeout:            time.Second,
		RequestTimeout:          time.Second,
		EventBufferSize:         defaultEventBufferSize,
		OperationWorkers:        defaultOperationWorkers,
		OperationQueueSize:      defaultOperationQueue,
		MaxOperationTimeout:     5 * time.Minute,
		MaxOperationInputBytes:  maxOperationInputBytes,
		MaxOperationOutputBytes: maxOperationOutputBytes,
		ResultPublishTimeout:    5 * time.Second,
		IdentityFile:            "./data/agent/identity.json",
		AgentVersion:            "test",
		RegistrationSubject:     "machinarchy.v1.agent.register",
		LeaseRenewSubject:       "machinarchy.v1.agent.lease.renew",
	}
}

func TestConfigRejectsWildcardControlPlaneSubject(t *testing.T) {
	cfg := validTestConfig()
	cfg.RegistrationSubject = "machinarchy.v1.agent.*"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want wildcard subject rejection")
	}
}

func TestLoadConfigUsesExplicitAgentStateFile(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv(envAgentNATSURL, "nats://127.0.0.1:4222")
	stateFile := filepath.Join(t.TempDir(), "custom", "identity.json")
	t.Setenv(envAgentStateFile, stateFile)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityFile != stateFile {
		t.Fatalf("IdentityFile = %q, want %q", cfg.IdentityFile, stateFile)
	}
}

func TestConfigRejectsInvalidEventBufferSize(t *testing.T) {
	cfg := validTestConfig()
	cfg.EventBufferSize = maxEventBufferSize + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want event buffer size validation error")
	}
}

func TestConfigRejectsGreaterThanInSubject(t *testing.T) {
	cfg := validTestConfig()
	cfg.RegistrationSubject = "machinarchy.v1.>"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() succeeded; want wildcard subject rejection")
	}
}

func TestConfigRejectsUnsafeOperationLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "too many workers", mutate: func(c *Config) { c.OperationWorkers = maxOperationWorkers + 1 }},
		{name: "queue too large", mutate: func(c *Config) { c.OperationQueueSize = maxOperationQueue + 1 }},
		{name: "timeout too long", mutate: func(c *Config) { c.MaxOperationTimeout = maxOperationTimeout + time.Second }},
		{name: "input limit too large", mutate: func(c *Config) { c.MaxOperationInputBytes = maxOperationInputBytes + 1 }},
		{name: "output limit too large", mutate: func(c *Config) { c.MaxOperationOutputBytes = maxOperationOutputBytes + 1 }},
		{name: "result timeout missing", mutate: func(c *Config) { c.ResultPublishTimeout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validTestConfig()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() succeeded for unsafe operation limits")
			}
		})
	}
}
