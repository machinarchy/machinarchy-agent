// Package servicesv1 defines transport-neutral contracts for service
// inspection and lifecycle operations performed by a host executor.
package servicesv1

import (
	"errors"
	"strings"
)

const (
	CapabilityStatus  = "service.status"
	CapabilityControl = "service.control"

	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"

	MaxServiceNameBytes = 255
)

// StatusRequest identifies one service. The executor must additionally check
// the service against its local allowlist before contacting the service manager.
type StatusRequest struct {
	Service string `json:"service"`
}

// StatusResponse contains a small, normalized subset of service-manager state.
type StatusResponse struct {
	Service       string `json:"service"`
	LoadState     string `json:"load_state"`
	ActiveState   string `json:"active_state"`
	SubState      string `json:"sub_state"`
	UnitFileState string `json:"unit_file_state,omitempty"`
	Description   string `json:"description,omitempty"`
}

// ControlRequest describes one fixed lifecycle action. It cannot carry
// arbitrary service-manager flags or command arguments.
type ControlRequest struct {
	Service string `json:"service"`
	Action  string `json:"action"`
}

// ControlResponse confirms that the service manager completed the requested
// lifecycle action. It does not claim that the application is healthy afterward.
type ControlResponse struct {
	Service string `json:"service"`
	Action  string `json:"action"`
	Result  string `json:"result"`
}

func ValidateServiceName(name string) error {
	if name == "" || len(name) > MaxServiceNameBytes || strings.TrimSpace(name) != name || !strings.HasSuffix(name, ".service") {
		return errors.New("service name must be a valid systemd .service unit name")
	}
	if name[0] == '-' || name[0] == '.' || strings.Contains(name, "..") {
		return errors.New("service name contains an invalid path or option-like prefix")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' && r != '@' {
			return errors.New("service name contains unsupported characters")
		}
	}
	return nil
}

func ValidateAction(action string) error {
	switch action {
	case ActionStart, ActionStop, ActionRestart:
		return nil
	default:
		return errors.New("service action must be start, stop, or restart")
	}
}
