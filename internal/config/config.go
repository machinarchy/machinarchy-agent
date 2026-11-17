// Package config loads and validates the agent's process configuration from
// environment variables. It never logs or returns secret values in errors.
package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Environment variable names.
const (
	EnvEnvironment             = "MACHINARCHY_ENVIRONMENT"
	EnvLogLevel                = "MACHINARCHY_LOG_LEVEL"
	EnvAgentName               = "MACHINARCHY_AGENT_NAME"
	EnvNATSURL                 = "MACHINARCHY_AGENT_NATS_URL"
	EnvNATSCredentialsFile     = "MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE" // #nosec G101 -- environment variable name, not a credential
	EnvNATSToken               = "MACHINARCHY_AGENT_NATS_TOKEN"            // #nosec G101 -- environment variable name, not a credential
	EnvNATSUsername            = "MACHINARCHY_AGENT_NATS_USERNAME"
	EnvNATSPassword            = "MACHINARCHY_AGENT_NATS_PASSWORD" // #nosec G101 -- environment variable name, not a credential
	EnvNATSCAFile              = "MACHINARCHY_AGENT_NATS_CA_FILE"
	EnvNATSCertFile            = "MACHINARCHY_AGENT_NATS_CERT_FILE"
	EnvNATSKeyFile             = "MACHINARCHY_AGENT_NATS_KEY_FILE"
	EnvConnectTimeout          = "MACHINARCHY_AGENT_CONNECT_TIMEOUT"
	EnvReconnectWait           = "MACHINARCHY_AGENT_RECONNECT_WAIT"
	EnvRequestTimeout          = "MACHINARCHY_AGENT_REQUEST_TIMEOUT"
	EnvDrainTimeout            = "MACHINARCHY_AGENT_DRAIN_TIMEOUT"
	EnvShutdownTimeout         = "MACHINARCHY_AGENT_SHUTDOWN_TIMEOUT"
	EnvStateFile               = "MACHINARCHY_AGENT_STATE_FILE"
	EnvControllerPublicKey     = "MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY"
	EnvControllerPublicKeyFile = "MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY_FILE"
	EnvEnrollmentToken         = "MACHINARCHY_AGENT_ENROLLMENT_TOKEN"      // #nosec G101 -- environment variable name, not a credential
	EnvEnrollmentTokenFile     = "MACHINARCHY_AGENT_ENROLLMENT_TOKEN_FILE" // #nosec G101 -- environment variable name, not a credential
	EnvOperationWorkers        = "MACHINARCHY_AGENT_OPERATION_WORKERS"
	EnvOperationQueueSize      = "MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE"
	EnvOperationTimeout        = "MACHINARCHY_AGENT_OPERATION_TIMEOUT"
	EnvOperationMaxOutput      = "MACHINARCHY_AGENT_OPERATION_MAX_OUTPUT_BYTES"
	EnvServiceManagement       = "MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED"
	EnvServiceControl          = "MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED"
	EnvServiceAllowlist        = "MACHINARCHY_AGENT_SERVICE_ALLOWLIST"
	EnvSystemctlPath           = "MACHINARCHY_AGENT_SYSTEMCTL_PATH"
)

// removedSettings were accepted by earlier, non-functional builds. Failing on
// them is safer than silently ignoring an operator's intent.
var removedSettings = []string{
	"MACHINARCHY_EVENT_BROKER_URL", "MACHINARCHY_NATS_URL", "MACHINARCHY_AGENT_VERSION",
	"MACHINARCHY_AGENT_STARTUP_TIMEOUT", "MACHINARCHY_AGENT_RECONNECT_JITTER",
	"MACHINARCHY_AGENT_REGISTER_SUBJECT", "MACHINARCHY_AGENT_LEASE_RENEW_SUBJECT",
	"MACHINARCHY_AGENT_EVENT_BUFFER_SIZE", "MACHINARCHY_AGENT_JETSTREAM_STREAM",
	"MACHINARCHY_AGENT_EVENT_NAK_DELAY", "MACHINARCHY_AGENT_OPERATION_MAX_INPUT_BYTES",
	"MACHINARCHY_AGENT_RESULT_PUBLISH_TIMEOUT", "MACHINARCHY_AGENT_OPERATION_STORE_FILE",
	"MACHINARCHY_AGENT_EXECUTION_ENABLED",
}

// Limits for operator-tunable values.
const (
	MaxWorkers        = 64
	MaxQueueSize      = 256
	DefaultOutputSize = 64 << 10
)

// Environments.
const (
	Production  = "production"
	Development = "development"
)

// NATS holds broker connection settings.
type NATS struct {
	URLs            []string
	ClientName      string
	CredentialsFile string
	Token           string
	Username        string
	Password        string
	CAFile          string
	CertFile        string
	KeyFile         string
	ConnectTimeout  time.Duration
	ReconnectWait   time.Duration
	DrainTimeout    time.Duration
}

// UsesTLS reports whether every endpoint is tls://.
func (n NATS) UsesTLS() bool {
	for _, u := range n.URLs {
		if !strings.HasPrefix(u, "tls://") {
			return false
		}
	}
	return len(n.URLs) > 0
}

// Config is the validated agent configuration.
type Config struct {
	Environment         string
	LogLevel            slog.Level
	NATS                NATS
	StateFile           string
	ControllerPublicKey ed25519.PublicKey
	EnrollmentToken     string
	RequestTimeout      time.Duration
	ShutdownTimeout     time.Duration
	Workers             int
	QueueSize           int
	MaxOperationTimeout time.Duration
	MaxOutputBytes      int
	ServiceEnabled      bool
	Services            capabilities.ServiceOptions
}

// Load reads configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
		return fallback
	}
	var removed []string
	for _, key := range removedSettings {
		if getenv(key) != "" {
			removed = append(removed, key)
		}
	}
	if len(removed) > 0 {
		return Config{}, fmt.Errorf("unsupported settings are set (remove them): %s", strings.Join(removed, ", "))
	}

	cfg := Config{
		Environment: strings.ToLower(get(EnvEnvironment, Production)),
		NATS: NATS{
			ClientName:      get(EnvAgentName, "machinarchy-agent"),
			CredentialsFile: get(EnvNATSCredentialsFile, ""),
			Token:           getenv(EnvNATSToken),
			Username:        getenv(EnvNATSUsername),
			Password:        getenv(EnvNATSPassword),
			CAFile:          get(EnvNATSCAFile, ""),
			CertFile:        get(EnvNATSCertFile, ""),
			KeyFile:         get(EnvNATSKeyFile, ""),
		},
		Services: capabilities.ServiceOptions{SystemctlPath: get(EnvSystemctlPath, "/usr/bin/systemctl")},
	}
	if cfg.Environment != Production && cfg.Environment != Development {
		return Config{}, fmt.Errorf("%s must be %q or %q", EnvEnvironment, Production, Development)
	}
	var err error
	if cfg.LogLevel, err = parseLevel(get(EnvLogLevel, "info")); err != nil {
		return Config{}, err
	}
	for _, raw := range strings.Split(getenv(EnvNATSURL), ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			cfg.NATS.URLs = append(cfg.NATS.URLs, raw)
		}
	}

	defaultState := "/var/lib/machinarchy-agent/state.json"
	if cfg.Environment == Development {
		defaultState = filepath.Join("data", "agent", "state.json")
	}
	if cfg.StateFile, err = filepath.Abs(get(EnvStateFile, defaultState)); err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvStateFile, err)
	}

	durations := []struct {
		key      string
		target   *time.Duration
		fallback time.Duration
		max      time.Duration
	}{
		{EnvConnectTimeout, &cfg.NATS.ConnectTimeout, 5 * time.Second, time.Minute},
		{EnvReconnectWait, &cfg.NATS.ReconnectWait, 2 * time.Second, time.Minute},
		{EnvDrainTimeout, &cfg.NATS.DrainTimeout, 5 * time.Second, time.Minute},
		{EnvRequestTimeout, &cfg.RequestTimeout, 5 * time.Second, time.Minute},
		{EnvShutdownTimeout, &cfg.ShutdownTimeout, 20 * time.Second, 5 * time.Minute},
		{EnvOperationTimeout, &cfg.MaxOperationTimeout, 5 * time.Minute, agentv1.MaxOperationTimeout},
	}
	for _, d := range durations {
		if *d.target, err = parseDuration(d.key, get(d.key, ""), d.fallback, d.max); err != nil {
			return Config{}, err
		}
	}
	ints := []struct {
		key           string
		target        *int
		fallback, max int
	}{
		{EnvOperationWorkers, &cfg.Workers, 4, MaxWorkers},
		{EnvOperationQueueSize, &cfg.QueueSize, 32, MaxQueueSize},
		{EnvOperationMaxOutput, &cfg.MaxOutputBytes, DefaultOutputSize, agentv1.MaxOperationOutput},
	}
	for _, i := range ints {
		if *i.target, err = parseInt(i.key, get(i.key, ""), i.fallback, i.max); err != nil {
			return Config{}, err
		}
	}
	if cfg.ServiceEnabled, err = parseBool(EnvServiceManagement, get(EnvServiceManagement, "false")); err != nil {
		return Config{}, err
	}
	if cfg.Services.ControlEnabled, err = parseBool(EnvServiceControl, get(EnvServiceControl, "false")); err != nil {
		return Config{}, err
	}
	for _, unit := range strings.Split(getenv(EnvServiceAllowlist), ",") {
		if unit = strings.TrimSpace(unit); unit != "" {
			cfg.Services.Allowed = append(cfg.Services.Allowed, unit)
		}
	}

	if cfg.ControllerPublicKey, err = loadControllerKey(getenv); err != nil {
		return Config{}, err
	}
	if cfg.EnrollmentToken, err = valueOrFile(getenv, EnvEnrollmentToken, EnvEnrollmentTokenFile, true); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

// Validate checks cross-field rules.
func (c Config) Validate() error {
	if len(c.NATS.URLs) == 0 {
		return fmt.Errorf("%s is required", EnvNATSURL)
	}
	for _, raw := range c.NATS.URLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "nats" && u.Scheme != "tls") || u.Hostname() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s entries must be nats://host:port or tls://host:port without credentials", EnvNATSURL)
		}
	}
	if !validLine(c.NATS.ClientName, 128) {
		return fmt.Errorf("%s must be a single line of at most 128 characters", EnvAgentName)
	}
	methods := 0
	for _, set := range []bool{c.NATS.CredentialsFile != "", c.NATS.Token != "", c.NATS.Username != "" || c.NATS.Password != ""} {
		if set {
			methods++
		}
	}
	if methods > 1 {
		return errors.New("configure exactly one NATS authentication method: credentials file, token, or username/password")
	}
	if (c.NATS.Username == "") != (c.NATS.Password == "") {
		return fmt.Errorf("%s and %s must be set together", EnvNATSUsername, EnvNATSPassword)
	}
	if (c.NATS.CertFile == "") != (c.NATS.KeyFile == "") {
		return fmt.Errorf("%s and %s must be set together", EnvNATSCertFile, EnvNATSKeyFile)
	}
	for key, path := range map[string]string{EnvNATSCredentialsFile: c.NATS.CredentialsFile, EnvNATSKeyFile: c.NATS.KeyFile} {
		if path != "" {
			if err := checkPrivateFile(key, path); err != nil {
				return err
			}
		}
	}
	for key, path := range map[string]string{EnvNATSCAFile: c.NATS.CAFile, EnvNATSCertFile: c.NATS.CertFile} {
		if path != "" {
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("%s must reference a readable regular file", key)
			}
		}
	}
	if c.Environment == Production {
		if !c.NATS.UsesTLS() {
			return fmt.Errorf("production requires tls:// endpoints in %s", EnvNATSURL)
		}
		if methods == 0 && c.NATS.CertFile == "" {
			return errors.New("production requires NATS authentication (credentials file, token, username/password, or client certificate)")
		}
	}
	if len(c.ControllerPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%s or %s is required", EnvControllerPublicKey, EnvControllerPublicKeyFile)
	}
	if c.EnrollmentToken != "" && (len(c.EnrollmentToken) < agentv1.MinEnrollmentToken || len(c.EnrollmentToken) > agentv1.MaxEnrollmentToken || !validLine(c.EnrollmentToken, agentv1.MaxEnrollmentToken)) {
		return fmt.Errorf("enrollment token must be %d-%d characters on one line", agentv1.MinEnrollmentToken, agentv1.MaxEnrollmentToken)
	}
	if c.Workers > c.QueueSize {
		return fmt.Errorf("%s must not exceed %s", EnvOperationWorkers, EnvOperationQueueSize)
	}
	if c.Services.ControlEnabled && !c.ServiceEnabled {
		return fmt.Errorf("%s requires %s=true", EnvServiceControl, EnvServiceManagement)
	}
	if c.ServiceEnabled && len(c.Services.Allowed) == 0 {
		return fmt.Errorf("%s requires %s", EnvServiceManagement, EnvServiceAllowlist)
	}
	if !c.ServiceEnabled && len(c.Services.Allowed) > 0 {
		return fmt.Errorf("%s is set but %s is not true", EnvServiceAllowlist, EnvServiceManagement)
	}
	for _, unit := range c.Services.Allowed {
		if err := agentv1.ValidateServiceName(unit); err != nil {
			return fmt.Errorf("%s entry %q: %w", EnvServiceAllowlist, unit, err)
		}
	}
	return nil
}

func loadControllerKey(getenv func(string) string) (ed25519.PublicKey, error) {
	raw, err := valueOrFile(getenv, EnvControllerPublicKey, EnvControllerPublicKeyFile, false)
	if err != nil || raw == "" {
		return nil, err
	}
	key, err := agentv1.ParsePublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("controller public key: %w", err)
	}
	return key, nil
}

// valueOrFile reads a value from key or from the file named by fileKey.
// Secret files must not be readable by group or others.
func valueOrFile(getenv func(string) string, key, fileKey string, secret bool) (string, error) {
	value, path := strings.TrimSpace(getenv(key)), strings.TrimSpace(getenv(fileKey))
	if value != "" && path != "" {
		return "", fmt.Errorf("set only one of %s and %s", key, fileKey)
	}
	if path == "" {
		return value, nil
	}
	if secret {
		if err := checkPrivateFile(fileKey, path); err != nil {
			return "", err
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured path.
	if err != nil {
		return "", fmt.Errorf("%s cannot be read", fileKey)
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("%s is too large", fileKey)
	}
	return strings.TrimSpace(string(data)), nil
}

func checkPrivateFile(key, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%s cannot be accessed", key)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s must be a non-empty regular file, not a symlink", key)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be accessible by group or others (chmod 0600)", key)
	}
	return nil
}

func validLine(value string, maxLength int) bool {
	return value != "" && len(value) <= maxLength && !strings.ContainsAny(value, "\r\n\x00") && strings.TrimSpace(value) == value
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("%s must be debug, info, warn, or error", EnvLogLevel)
}

func parseDuration(key, value string, fallback, maxValue time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d > maxValue {
		return 0, fmt.Errorf("%s must be a duration between 1ns and %s", key, maxValue)
	}
	return d, nil
}

func parseInt(key, value string, fallback, maxValue int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > maxValue {
		return 0, fmt.Errorf("%s must be an integer between 1 and %d", key, maxValue)
	}
	return n, nil
}

func parseBool(key, value string) (bool, error) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return b, nil
}
