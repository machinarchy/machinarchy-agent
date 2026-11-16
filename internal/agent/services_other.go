//go:build !linux

package agent

import "errors"

func newServiceCapabilities(ServiceCapabilityConfig) (Capability, Capability, error) {
	return nil, nil, errors.New("service management is currently supported only on Linux with systemd")
}
