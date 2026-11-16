package capabilities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	servicesv1 "github.com/machinarchy/machinarchy-protocol/services/v1"
)

const maxSystemctlOutputBytes = 8 * 1024

// ServiceCapabilityConfig is the local security policy for service operations.
// The controller can select only a unit in AllowedServices and cannot supply
// systemctl flags, executable paths, or arbitrary arguments.
type ServiceCapabilityConfig struct {
	SystemctlPath   string
	AllowedServices []string
	ControlEnabled  bool
}

// newPlatformServiceCapabilities is implemented by the platform-specific
// service manager adapter. The initial production backend is systemd on Linux.
func newPlatformServiceCapabilities(config ServiceCapabilityConfig) (Capability, Capability, error) {
	return newServiceCapabilities(config)
}

func validateServiceAllowlist(names []string) (map[string]struct{}, error) {
	if len(names) == 0 || len(names) > config.MaxAllowedServices {
		return nil, fmt.Errorf("service allowlist must contain between 1 and %d units", config.MaxAllowedServices)
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		if err := servicesv1.ValidateServiceName(name); err != nil {
			return nil, errors.New("service allowlist contains an invalid unit name")
		}
		if _, exists := allowed[name]; exists {
			return nil, errors.New("service allowlist contains duplicate unit names")
		}
		allowed[name] = struct{}{}
	}
	return allowed, nil
}

func decodeServiceRequest(input json.RawMessage, target any) error {
	if len(input) == 0 || len(input) > 4*1024 {
		return errors.New("service request is empty or too large")
	}
	if err := decodeStrictObject(input, target, 4*1024); err != nil {
		return errors.New("service request is invalid")
	}
	return nil
}

func validateAllowedService(name string, allowed map[string]struct{}) error {
	if err := servicesv1.ValidateServiceName(name); err != nil {
		return err
	}
	if _, ok := allowed[name]; !ok {
		return errors.New("service is not permitted by the local Agent allowlist")
	}
	return nil
}

func serviceStatusCapability(manager systemServiceManager, allowed map[string]struct{}) Capability {
	return CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if ctx == nil {
			return nil, errors.New("service status context is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var request servicesv1.StatusRequest
		if err := decodeServiceRequest(input, &request); err != nil {
			return nil, err
		}
		if err := validateAllowedService(request.Service, allowed); err != nil {
			return nil, err
		}
		status, err := manager.Status(ctx, request.Service)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	})
}

func serviceControlCapability(manager systemServiceManager, allowed map[string]struct{}) Capability {
	return CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if ctx == nil {
			return nil, errors.New("service control context is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var request servicesv1.ControlRequest
		if err := decodeServiceRequest(input, &request); err != nil {
			return nil, err
		}
		if err := validateAllowedService(request.Service, allowed); err != nil {
			return nil, err
		}
		if err := servicesv1.ValidateAction(request.Action); err != nil {
			return nil, err
		}
		if err := manager.Control(ctx, request.Service, request.Action); err != nil {
			return nil, err
		}
		return json.Marshal(servicesv1.ControlResponse{
			Service: request.Service,
			Action:  request.Action,
			Result:  "completed",
		})
	})
}

type systemServiceManager interface {
	Status(context.Context, string) (servicesv1.StatusResponse, error)
	Control(context.Context, string, string) error
}

func decodeStrictObject(payload []byte, target any, limit int) error {
	if len(payload) == 0 || len(payload) > limit {
		return errors.New("payload size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
