package capabilities

import (
	"fmt"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	servicesv1 "github.com/machinarchy/machinarchy-agent/protocol/services/v1"
)

// NewDefaultRegistry builds only the Agent's approved baseline capabilities
// and adds systemd service operations when explicitly enabled in local config.
func NewDefaultRegistry(agentVersion string, cfg config.Config) (*Registry, error) {
	registered := []string{"runtime.info", "system.facts"}
	if cfg.ServiceManagementEnabled {
		registered = append(registered, servicesv1.CapabilityStatus)
		if cfg.ServiceControlEnabled {
			registered = append(registered, servicesv1.CapabilityControl)
		}
	}

	registry := NewRegistry()
	if err := registry.Register("runtime.info", runtimeInfoCapability(agentVersion, registered)); err != nil {
		return nil, err
	}
	if err := registry.Register("system.facts", SystemFactsCapability(agentVersion)); err != nil {
		return nil, err
	}
	if cfg.ServiceManagementEnabled {
		statusCapability, controlCapability, err := newPlatformServiceCapabilities(ServiceCapabilityConfig{
			SystemctlPath: cfg.SystemctlPath, AllowedServices: cfg.AllowedServices,
			ControlEnabled: cfg.ServiceControlEnabled,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize service management capabilities: %w", err)
		}
		if err := registry.Register(servicesv1.CapabilityStatus, statusCapability); err != nil {
			return nil, err
		}
		if cfg.ServiceControlEnabled {
			if err := registry.Register(servicesv1.CapabilityControl, controlCapability); err != nil {
				return nil, err
			}
		}
	}
	return registry, nil
}
