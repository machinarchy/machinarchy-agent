// Package agent contains the Machinarchy agent process runtime.
package agent

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy-protocol/agent/v1"
	servicesv1 "github.com/machinarchy/machinarchy-protocol/services/v1"
)

const (
	maxAllowedServices          = 64
	envAgentNATSURL             = "MACHINARCHY_AGENT_NATS_URL"
	envEventBrokerURL           = "MACHINARCHY_EVENT_BROKER_URL"
	envNATSURL                  = "MACHINARCHY_NATS_URL"
	envAgentName                = "MACHINARCHY_AGENT_NAME"
	envEnvironment              = "MACHINARCHY_ENVIRONMENT"
	envNATSCredentialsFile      = "MACHINARCHY_AGENT_NATS_CREDENTIALS_FILE"
	envNATSToken                = "MACHINARCHY_AGENT_NATS_TOKEN"
	envNATSUsername             = "MACHINARCHY_AGENT_NATS_USERNAME"
	envNATSPassword             = "MACHINARCHY_AGENT_NATS_PASSWORD"
	envConnectTimeout           = "MACHINARCHY_AGENT_CONNECT_TIMEOUT"
	envStartupTimeout           = "MACHINARCHY_AGENT_STARTUP_TIMEOUT"
	envReconnectWait            = "MACHINARCHY_AGENT_RECONNECT_WAIT"
	envReconnectJitter          = "MACHINARCHY_AGENT_RECONNECT_JITTER"
	envDrainTimeout             = "MACHINARCHY_AGENT_DRAIN_TIMEOUT"
	envAgentStateFile           = "MACHINARCHY_AGENT_STATE_FILE"
	envOperationStoreFile       = "MACHINARCHY_AGENT_OPERATION_STORE_FILE"
	envAgentEnrollmentToken     = "MACHINARCHY_AGENT_ENROLLMENT_TOKEN"
	envAgentVersion             = "MACHINARCHY_AGENT_VERSION"
	envRegisterSubject          = "MACHINARCHY_AGENT_REGISTER_SUBJECT"
	envLeaseRenewSubject        = "MACHINARCHY_AGENT_LEASE_RENEW_SUBJECT"
	envRequestTimeout           = "MACHINARCHY_AGENT_REQUEST_TIMEOUT"
	envEventBufferSize          = "MACHINARCHY_AGENT_EVENT_BUFFER_SIZE"
	envJetStreamStream          = "MACHINARCHY_AGENT_JETSTREAM_STREAM"
	envEventNakDelay            = "MACHINARCHY_AGENT_EVENT_NAK_DELAY"
	envOperationWorkers         = "MACHINARCHY_AGENT_OPERATION_WORKERS"
	envOperationQueueSize       = "MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE"
	envOperationTimeout         = "MACHINARCHY_AGENT_OPERATION_TIMEOUT"
	envOperationInputLimit      = "MACHINARCHY_AGENT_OPERATION_MAX_INPUT_BYTES"
	envOperationOutputLimit     = "MACHINARCHY_AGENT_OPERATION_MAX_OUTPUT_BYTES"
	envResultPublishTimeout     = "MACHINARCHY_AGENT_RESULT_PUBLISH_TIMEOUT"
	envExecutionEnabled         = "MACHINARCHY_AGENT_EXECUTION_ENABLED"
	envExecutionShellPath       = "MACHINARCHY_AGENT_EXECUTION_SHELL"
	envServiceManagementEnabled = "MACHINARCHY_AGENT_SERVICE_MANAGEMENT_ENABLED"
	envServiceControlEnabled    = "MACHINARCHY_AGENT_SERVICE_CONTROL_ENABLED"
	envServiceAllowlist         = "MACHINARCHY_AGENT_SERVICE_ALLOWLIST"
	envSystemctlPath            = "MACHINARCHY_AGENT_SYSTEMCTL_PATH"
)

// Config contains process-local settings needed to establish the agent's
// outbound NATS connection. It intentionally does not contain an agent ID:
// agent identity must be assigned by the control plane, not self-asserted here.
type Config struct {
	Environment string
	LogLevel    string
	ClientName  string
	BrokerURLs  string

	CredentialsFile string
	Token           string
	Username        string
	Password        string

	ConnectTimeout  time.Duration
	StartupTimeout  time.Duration
	ReconnectWait   time.Duration
	ReconnectJitter time.Duration
	DrainTimeout    time.Duration

	// IdentityFile stores the control-plane-assigned identity and current lease
	// credential. The file is created with restrictive permissions.
	IdentityFile       string
	OperationStoreFile string

	// EnrollmentToken is a one-time bootstrap credential. It is never persisted
	// by the agent and is only used when no registered identity exists.
	EnrollmentToken string
	AgentVersion    string

	RegistrationSubject     string
	LeaseRenewSubject       string
	RequestTimeout          time.Duration
	EventBufferSize         int
	JetStreamStream         string
	EventNakDelay           time.Duration
	OperationWorkers        int
	OperationQueueSize      int
	MaxOperationTimeout     time.Duration
	MaxOperationInputBytes  int
	MaxOperationOutputBytes int
	ResultPublishTimeout    time.Duration

	// CommandExecutionEnabled opts this host into controller-requested command
	// and shell-script execution. It is disabled by default.
	CommandExecutionEnabled bool
	ExecutionShellPath      string

	// Service management is separately opt-in and restricted to exact local
	// systemd unit names. Mutating actions require an additional opt-in.
	ServiceManagementEnabled bool
	ServiceControlEnabled    bool
	AllowedServices          []string
	SystemctlPath            string
}

// LoadConfig loads and validates agent configuration from the process
// environment. It does not log configuration values or exit the process.
func LoadConfig() (Config, error) {
	get := func(key, fallback string) string {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			return fallback
		}
		return value
	}

	brokerURL := firstNonEmpty(
		os.Getenv(envAgentNATSURL),
		os.Getenv(envEventBrokerURL),
		os.Getenv(envNATSURL),
	)

	cfg := Config{
		Environment:              strings.ToLower(get(envEnvironment, "development")),
		LogLevel:                 strings.ToLower(get("MACHINARCHY_LOG_LEVEL", "info")),
		ClientName:               get(envAgentName, "machinarchy-agent"),
		BrokerURLs:               strings.TrimSpace(brokerURL),
		CredentialsFile:          strings.TrimSpace(os.Getenv(envNATSCredentialsFile)),
		Token:                    os.Getenv(envNATSToken),
		Username:                 os.Getenv(envNATSUsername),
		Password:                 os.Getenv(envNATSPassword),
		ConnectTimeout:           5 * time.Second,
		StartupTimeout:           30 * time.Second,
		ReconnectWait:            2 * time.Second,
		ReconnectJitter:          500 * time.Millisecond,
		DrainTimeout:             10 * time.Second,
		IdentityFile:             get(envAgentStateFile, defaultIdentityFile(strings.ToLower(get(envEnvironment, "development")))),
		OperationStoreFile:       strings.TrimSpace(os.Getenv(envOperationStoreFile)),
		EnrollmentToken:          os.Getenv(envAgentEnrollmentToken),
		AgentVersion:             get(envAgentVersion, "dev"),
		RegistrationSubject:      get(envRegisterSubject, agentv1.SubjectRegisterAgent),
		LeaseRenewSubject:        get(envLeaseRenewSubject, agentv1.SubjectRenewLease),
		RequestTimeout:           5 * time.Second,
		EventBufferSize:          defaultEventBufferSize,
		JetStreamStream:          agentv1.AgentEventsStreamName,
		EventNakDelay:            5 * time.Second,
		OperationWorkers:         defaultOperationWorkers,
		OperationQueueSize:       defaultOperationQueue,
		MaxOperationTimeout:      5 * time.Minute,
		MaxOperationInputBytes:   maxOperationInputBytes,
		MaxOperationOutputBytes:  maxOperationOutputBytes,
		ResultPublishTimeout:     5 * time.Second,
		CommandExecutionEnabled:  false,
		ExecutionShellPath:       "/bin/sh",
		ServiceManagementEnabled: false,
		ServiceControlEnabled:    false,
		SystemctlPath:            "/usr/bin/systemctl",
	}

	if cfg.OperationStoreFile == "" {
		cfg.OperationStoreFile = cfg.IdentityFile + ".operations.json"
	}

	var err error
	if cfg.ConnectTimeout, err = durationFromEnv(envConnectTimeout, cfg.ConnectTimeout); err != nil {
		return Config{}, err
	}
	if cfg.StartupTimeout, err = durationFromEnv(envStartupTimeout, cfg.StartupTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReconnectWait, err = durationFromEnv(envReconnectWait, cfg.ReconnectWait); err != nil {
		return Config{}, err
	}
	if cfg.ReconnectJitter, err = nonNegativeDurationFromEnv(envReconnectJitter, cfg.ReconnectJitter); err != nil {
		return Config{}, err
	}
	if cfg.DrainTimeout, err = durationFromEnv(envDrainTimeout, cfg.DrainTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = durationFromEnv(envRequestTimeout, cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.EventBufferSize, err = positiveIntFromEnv(envEventBufferSize, cfg.EventBufferSize); err != nil {
		return Config{}, err
	}
	cfg.JetStreamStream = get(envJetStreamStream, cfg.JetStreamStream)
	if cfg.EventNakDelay, err = durationFromEnv(envEventNakDelay, cfg.EventNakDelay); err != nil {
		return Config{}, err
	}
	if cfg.OperationWorkers, err = positiveIntFromEnv(envOperationWorkers, cfg.OperationWorkers); err != nil {
		return Config{}, err
	}
	if cfg.OperationQueueSize, err = positiveIntFromEnv(envOperationQueueSize, cfg.OperationQueueSize); err != nil {
		return Config{}, err
	}
	if cfg.MaxOperationTimeout, err = durationFromEnv(envOperationTimeout, cfg.MaxOperationTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MaxOperationInputBytes, err = positiveIntFromEnv(envOperationInputLimit, cfg.MaxOperationInputBytes); err != nil {
		return Config{}, err
	}
	if cfg.MaxOperationOutputBytes, err = positiveIntFromEnv(envOperationOutputLimit, cfg.MaxOperationOutputBytes); err != nil {
		return Config{}, err
	}
	if cfg.ResultPublishTimeout, err = durationFromEnv(envResultPublishTimeout, cfg.ResultPublishTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CommandExecutionEnabled, err = boolFromEnv(envExecutionEnabled, cfg.CommandExecutionEnabled); err != nil {
		return Config{}, err
	}
	cfg.ExecutionShellPath = strings.TrimSpace(get(envExecutionShellPath, cfg.ExecutionShellPath))
	if cfg.ServiceManagementEnabled, err = boolFromEnv(envServiceManagementEnabled, cfg.ServiceManagementEnabled); err != nil {
		return Config{}, err
	}
	if cfg.ServiceControlEnabled, err = boolFromEnv(envServiceControlEnabled, cfg.ServiceControlEnabled); err != nil {
		return Config{}, err
	}
	cfg.SystemctlPath = strings.TrimSpace(get(envSystemctlPath, cfg.SystemctlPath))
	if raw := strings.TrimSpace(os.Getenv(envServiceAllowlist)); raw != "" {
		for _, name := range strings.Split(raw, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				return Config{}, fmt.Errorf("%s must be a comma-separated list without empty entries", envServiceAllowlist)
			}
			cfg.AllowedServices = append(cfg.AllowedServices, name)
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration without exposing secret values in errors.
func (c Config) Validate() error {
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		return fmt.Errorf("%s must be development, test, or production", envEnvironment)
	}
	if strings.TrimSpace(c.ClientName) == "" || strings.TrimSpace(c.ClientName) != c.ClientName || strings.ContainsAny(c.ClientName, "\r\n\x00") {
		return fmt.Errorf("%s must be a non-empty single-line value without surrounding whitespace", envAgentName)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("MACHINARCHY_LOG_LEVEL must be debug, info, warn, or error")
	}
	if c.BrokerURLs == "" {
		return fmt.Errorf("%s (or %s) is required", envAgentNATSURL, envEventBrokerURL)
	}
	if strings.TrimSpace(c.IdentityFile) == "" || strings.TrimSpace(c.IdentityFile) != c.IdentityFile || strings.ContainsAny(c.IdentityFile, "\r\n\x00") {
		return errors.New("agent identity file path must be valid and non-empty")
	}
	if c.OperationStoreFile != "" {
		if strings.TrimSpace(c.OperationStoreFile) != c.OperationStoreFile || strings.ContainsAny(c.OperationStoreFile, "\r\n\x00") {
			return errors.New("agent operation store path is invalid")
		}
		if filepath.Clean(c.IdentityFile) == filepath.Clean(c.OperationStoreFile) {
			return errors.New("agent identity and operation store must use different files")
		}
	}
	if c.ConnectTimeout <= 0 || c.StartupTimeout <= 0 || c.ReconnectWait <= 0 || c.DrainTimeout <= 0 || c.RequestTimeout <= 0 || c.ReconnectJitter < 0 {
		return errors.New("agent timeouts must be positive; reconnect jitter cannot be negative")
	}
	if c.EventBufferSize < 1 || c.EventBufferSize > maxEventBufferSize {
		return fmt.Errorf("%s must be between 1 and %d", envEventBufferSize, maxEventBufferSize)
	}
	if !validJetStreamStreamName(c.JetStreamStream) {
		return fmt.Errorf("%s must be a valid JetStream stream name", envJetStreamStream)
	}
	if c.EventNakDelay <= 0 || c.EventNakDelay > 5*time.Minute {
		return fmt.Errorf("%s must be between 1ns and 5m", envEventNakDelay)
	}
	if c.OperationWorkers < 1 || c.OperationWorkers > maxOperationWorkers {
		return fmt.Errorf("%s must be between 1 and %d", envOperationWorkers, maxOperationWorkers)
	}
	if c.OperationQueueSize < 1 || c.OperationQueueSize > maxOperationQueue {
		return fmt.Errorf("%s must be between 1 and %d", envOperationQueueSize, maxOperationQueue)
	}
	if c.MaxOperationTimeout <= 0 || c.MaxOperationTimeout > maxOperationTimeout {
		return fmt.Errorf("%s must be positive and no greater than %s", envOperationTimeout, maxOperationTimeout)
	}
	if c.MaxOperationInputBytes < 1 || c.MaxOperationInputBytes > maxOperationInputBytes {
		return fmt.Errorf("%s must be between 1 and %d", envOperationInputLimit, maxOperationInputBytes)
	}
	if c.MaxOperationOutputBytes < 1 || c.MaxOperationOutputBytes > maxOperationOutputBytes {
		return fmt.Errorf("%s must be between 1 and %d", envOperationOutputLimit, maxOperationOutputBytes)
	}
	if c.ResultPublishTimeout <= 0 {
		return fmt.Errorf("%s must be positive", envResultPublishTimeout)
	}
	if c.ServiceControlEnabled && !c.ServiceManagementEnabled {
		return fmt.Errorf("%s requires %s=true", envServiceControlEnabled, envServiceManagementEnabled)
	}
	if c.ServiceManagementEnabled {
		if len(c.AllowedServices) == 0 || len(c.AllowedServices) > maxAllowedServices {
			return fmt.Errorf("%s requires between 1 and %d exact .service unit names", envServiceAllowlist, maxAllowedServices)
		}
		if c.SystemctlPath == "" || !filepath.IsAbs(c.SystemctlPath) || strings.ContainsAny(c.SystemctlPath, "\r\n\x00") {
			return fmt.Errorf("%s must be an absolute single-line executable path", envSystemctlPath)
		}
		seen := make(map[string]struct{}, len(c.AllowedServices))
		for _, name := range c.AllowedServices {
			if err := servicesv1.ValidateServiceName(name); err != nil {
				return fmt.Errorf("%s contains an invalid unit name", envServiceAllowlist)
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("%s contains duplicate unit names", envServiceAllowlist)
			}
			seen[name] = struct{}{}
		}
	} else if len(c.AllowedServices) != 0 {
		return fmt.Errorf("%s requires %s=true", envServiceAllowlist, envServiceManagementEnabled)
	}
	if c.CommandExecutionEnabled {
		if c.MaxOperationOutputBytes < minimumExecutionOutputBytes {
			return fmt.Errorf("%s must be at least %d when command execution is enabled", envOperationOutputLimit, minimumExecutionOutputBytes)
		}
		if c.ExecutionShellPath == "" || !filepath.IsAbs(c.ExecutionShellPath) || strings.ContainsAny(c.ExecutionShellPath, "\r\n\x00") {
			return fmt.Errorf("%s must be an absolute single-line path when command execution is enabled", envExecutionShellPath)
		}
	}
	if c.IdentityFile == "" || strings.TrimSpace(c.IdentityFile) != c.IdentityFile || strings.ContainsAny(c.IdentityFile, "\r\n\x00") {
		return errors.New("agent identity file path must be a non-empty single-line path")
	}
	if len(c.EnrollmentToken) > 256 || strings.TrimSpace(c.EnrollmentToken) != c.EnrollmentToken || strings.ContainsAny(c.EnrollmentToken, "\r\n\x00") {
		return errors.New("agent enrollment token has an invalid format")
	}
	if len(c.EnrollmentToken) > 0 && len(c.EnrollmentToken) < 32 {
		return errors.New("agent enrollment token must contain at least 32 bytes")
	}
	if c.AgentVersion == "" || len(c.AgentVersion) > 64 || strings.TrimSpace(c.AgentVersion) != c.AgentVersion || strings.ContainsAny(c.AgentVersion, "\r\n\x00") {
		return errors.New("agent version must be a non-empty single-line value of at most 64 bytes")
	}
	if err := validateNATSSubject(envRegisterSubject, c.RegistrationSubject); err != nil {
		return err
	}
	if err := validateNATSSubject(envLeaseRenewSubject, c.LeaseRenewSubject); err != nil {
		return err
	}

	usesTLS, err := validateBrokerURLs(c.BrokerURLs)
	if err != nil {
		return err
	}
	if c.Environment == "production" && !usesTLS {
		return errors.New("production agent connections require tls:// NATS endpoints")
	}

	if strings.TrimSpace(c.Token) != c.Token || strings.ContainsAny(c.Token, "\r\n\x00") {
		return fmt.Errorf("%s contains invalid whitespace", envNATSToken)
	}
	if strings.TrimSpace(c.Username) != c.Username || strings.TrimSpace(c.Password) != c.Password || strings.ContainsAny(c.Username+c.Password, "\r\n\x00") {
		return errors.New("NATS username/password contain invalid whitespace")
	}

	usernameSet := c.Username != ""
	passwordSet := c.Password != ""
	if usernameSet != passwordSet {
		return fmt.Errorf("%s and %s must be configured together", envNATSUsername, envNATSPassword)
	}

	authMethods := 0
	if c.CredentialsFile != "" {
		authMethods++
	}
	if c.Token != "" {
		authMethods++
	}
	if usernameSet && passwordSet {
		authMethods++
	}
	if authMethods > 1 {
		return errors.New("configure only one NATS authentication method: credentials file, token, or username/password")
	}
	if c.Environment == "production" && authMethods == 0 {
		return errors.New("production agent connections require NATS authentication")
	}

	if c.CredentialsFile != "" {
		path := expandHome(c.CredentialsFile)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("%s cannot be accessed: %w", envNATSCredentialsFile, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%s must reference a regular file, not a symlink", envNATSCredentialsFile)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%s must not be accessible by group or others (use chmod 0600)", envNATSCredentialsFile)
		}
		if info.Size() == 0 {
			return fmt.Errorf("%s must not reference an empty file", envNATSCredentialsFile)
		}
	}

	return nil
}

func validateBrokerURLs(raw string) (bool, error) {
	parts := strings.Split(raw, ",")
	allTLS := true
	for _, part := range parts {
		endpoint := strings.TrimSpace(part)
		if endpoint == "" {
			return false, fmt.Errorf("%s contains an empty endpoint", envAgentNATSURL)
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return false, fmt.Errorf("%s contains an invalid endpoint", envAgentNATSURL)
		}
		if u.Scheme != "nats" && u.Scheme != "tls" {
			return false, fmt.Errorf("%s endpoints must use nats:// or tls://", envAgentNATSURL)
		}
		if u.Hostname() == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			return false, fmt.Errorf("%s endpoints must contain a host only; credentials must use dedicated authentication settings", envAgentNATSURL)
		}
		if port := u.Port(); port != "" {
			number, parseErr := strconv.Atoi(port)
			if parseErr != nil || number < 1 || number > 65535 {
				return false, fmt.Errorf("%s contains an invalid endpoint port", envAgentNATSURL)
			}
		}
		if u.Scheme != "tls" {
			allTLS = false
		}
	}
	return allTLS, nil
}

func positiveIntFromEnv(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func boolFromEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return d, nil
}

func nonNegativeDurationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s must be a non-negative duration", key)
	}
	return d, nil
}

func (c Config) normalizedBrokerURLs() string {
	parts := strings.Split(c.BrokerURLs, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func defaultIdentityFile(environment string) string {
	if environment == "production" {
		return "/var/lib/machinarchy-agent/identity.json"
	}
	return "./data/agent/identity.json"
}

func validateNATSSubject(name, subject string) error {
	if subject == "" || len(subject) > 255 || strings.TrimSpace(subject) != subject || strings.ContainsAny(subject, "\r\n\x00 *,>") {
		return fmt.Errorf("%s must be a valid literal NATS subject", name)
	}
	for _, part := range strings.Split(subject, ".") {
		if part == "" {
			return fmt.Errorf("%s must not contain empty subject tokens", name)
		}
	}
	return nil
}

// validJetStreamStreamName applies the conservative subset of NATS stream-name
// characters accepted by JetStream. It deliberately rejects dots and wildcard
// tokens, which are meaningful in subjects but not in stream names.
func validJetStreamStreamName(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
