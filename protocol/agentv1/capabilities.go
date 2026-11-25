package agentv1

import (
	"errors"
	"strings"
	"time"
)

// Capability names supported by this protocol version.
const (
	CapabilityRuntimeInfo    = "runtime.info"
	CapabilitySystemFacts    = "system.facts"
	CapabilityServiceStatus  = "service.status"
	CapabilityServiceControl = "service.control"
)

// ValidCapabilityName reports whether name is syntactically valid
// (dot-separated identifiers).
func ValidCapabilityName(name string) bool {
	if name == "" || len(name) > MaxIdentifierLength {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if !ValidIdentifier(part) {
			return false
		}
	}
	return true
}

// RuntimeInfo is the output of runtime.info (input: {}).
type RuntimeInfo struct {
	AgentVersion string   `json:"agent_version"`
	GoVersion    string   `json:"go_version"`
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	CPUCount     int      `json:"cpu_count"`
	Capabilities []string `json:"capabilities"`
}

// SystemFacts is the output of system.facts (input: {}). Optional fields are
// omitted when the platform cannot report them.
type SystemFacts struct {
	CollectedAt   time.Time        `json:"collected_at"`
	Hostname      string           `json:"hostname"`
	OS            OperatingSystem  `json:"os"`
	Architecture  string           `json:"architecture"`
	LogicalCPUs   int              `json:"logical_cpus"`
	Memory        *CapacityFacts   `json:"memory,omitempty"`
	RootFS        *CapacityFacts   `json:"root_fs,omitempty"`
	UptimeSeconds *uint64          `json:"uptime_seconds,omitempty"`
	Agent         AgentRuntimeInfo `json:"agent"`
}

// OperatingSystem identifies the host OS.
type OperatingSystem struct {
	Platform string `json:"platform"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
}

// CapacityFacts reports total and available bytes.
type CapacityFacts struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// AgentRuntimeInfo identifies the reporting agent build.
type AgentRuntimeInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
}

// ServiceStatusRequest is the input of service.status.
type ServiceStatusRequest struct {
	Service string `json:"service"`
}

// ServiceStatus is the output of service.status.
type ServiceStatus struct {
	Service       string `json:"service"`
	LoadState     string `json:"load_state"`
	ActiveState   string `json:"active_state"`
	SubState      string `json:"sub_state"`
	UnitFileState string `json:"unit_file_state,omitempty"`
	Description   string `json:"description,omitempty"`
}

// ServiceControlRequest is the input of service.control.
type ServiceControlRequest struct {
	Service string `json:"service"`
	Action  string `json:"action"`
}

// ServiceControlResponse is the output of service.control.
type ServiceControlResponse struct {
	Service string `json:"service"`
	Action  string `json:"action"`
	Result  string `json:"result"`
}

// Service actions accepted by service.control. Anything else is rejected
// before reaching systemctl.
const (
	ServiceActionStart   = "start"
	ServiceActionStop    = "stop"
	ServiceActionRestart = "restart"
)

// ValidateServiceAction accepts only the fixed lifecycle verbs.
func ValidateServiceAction(action string) error {
	switch action {
	case ServiceActionStart, ServiceActionStop, ServiceActionRestart:
		return nil
	}
	return errors.New("service action must be start, stop, or restart")
}

// ValidateServiceName accepts exact systemd service unit names such as
// "nginx.service" or "getty@tty1.service".
func ValidateServiceName(name string) error {
	if len(name) <= len(".service") || len(name) > 255 || !strings.HasSuffix(name, ".service") ||
		strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return errors.New("service must be an exact .service unit name")
	}
	for _, r := range name {
		if !isIdentifierRune(r) && r != '.' && r != '@' {
			return errors.New("service unit name contains an invalid character")
		}
	}
	return nil
}
