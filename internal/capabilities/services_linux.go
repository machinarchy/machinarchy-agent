//go:build linux

package capabilities

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	servicesv1 "github.com/machinarchy/machinarchy-agent/protocol/services/v1"
)

type systemdServiceManager struct {
	path string

	lockMu      sync.Mutex
	controlLock map[string]chan struct{}
}

func newServiceCapabilities(config ServiceCapabilityConfig) (Capability, Capability, error) {
	allowed, err := validateServiceAllowlist(config.AllowedServices)
	if err != nil {
		return nil, nil, err
	}
	if config.SystemctlPath == "" || !strings.HasPrefix(config.SystemctlPath, "/") || strings.ContainsAny(config.SystemctlPath, "\r\n\x00") {
		return nil, nil, errors.New("systemctl path must be an absolute single-line path")
	}
	info, err := os.Stat(config.SystemctlPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, nil, errors.New("configured systemctl path is not an executable regular file")
	}
	manager := &systemdServiceManager{path: config.SystemctlPath, controlLock: make(map[string]chan struct{}, len(allowed))}
	status := serviceStatusCapability(manager, allowed)
	var control Capability
	if config.ControlEnabled {
		control = serviceControlCapability(manager, allowed)
	}
	return status, control, nil
}

func (m *systemdServiceManager) Status(ctx context.Context, service string) (servicesv1.StatusResponse, error) {
	cmd := exec.CommandContext(ctx, m.path,
		"show", "--no-pager",
		"--property=Id,LoadState,ActiveState,SubState,UnitFileState,Description",
		service,
	)
	stdout := &limitedOutputBuffer{limit: maxSystemctlOutputBytes}
	stderr := &limitedOutputBuffer{limit: 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = serviceManagerEnvironment()
	cmd.Dir = string(os.PathSeparator)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return servicesv1.StatusResponse{}, ctx.Err()
		}
		return servicesv1.StatusResponse{}, fmt.Errorf("systemd status query failed: %w", safeExitError(err))
	}
	if stdout.truncated || stderr.truncated {
		return servicesv1.StatusResponse{}, errors.New("systemd status response exceeded its output limit")
	}
	return parseSystemdStatus(service, stdout.String())
}

func (m *systemdServiceManager) Control(ctx context.Context, service, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock := m.serviceControlLock(service)
	select {
	case lock <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock }()

	// Serialize mutations for a unit so concurrent controller operations do not
	// race each other. Waiting for the per-unit lock consumes the operation's
	// normal deadline and is cancellable.
	// The action is an enum and the unit is checked against the local allowlist
	// before this call. No shell, flags, or controller-supplied extra arguments.
	cmd := exec.CommandContext(ctx, m.path, action, service)
	stdout := &limitedOutputBuffer{limit: 1024}
	stderr := &limitedOutputBuffer{limit: 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = serviceManagerEnvironment()
	cmd.Dir = string(os.PathSeparator)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("systemd service action %q failed: %w", action, safeExitError(err))
	}
	if stdout.truncated || stderr.truncated {
		// A large diagnostic should not be interpreted as a clean operation even
		// when systemctl returned zero; it may indicate an unexpected wrapper.
		return errors.New("systemd service action produced excessive output")
	}
	return nil
}

func parseSystemdStatus(service, output string) (servicesv1.StatusResponse, error) {
	result := servicesv1.StatusResponse{Service: service}
	seen := make(map[string]bool, 6)
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 256), 2048)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return servicesv1.StatusResponse{}, errors.New("systemd status response is malformed")
		}
		if seen[key] {
			return servicesv1.StatusResponse{}, errors.New("systemd status response contains duplicate properties")
		}
		seen[key] = true
		switch key {
		case "Id":
			if value != service {
				return servicesv1.StatusResponse{}, errors.New("systemd returned an unexpected unit identity")
			}
		case "LoadState":
			result.LoadState = value
		case "ActiveState":
			result.ActiveState = value
		case "SubState":
			result.SubState = value
		case "UnitFileState":
			result.UnitFileState = value
		case "Description":
			if len(value) > 1024 {
				return servicesv1.StatusResponse{}, errors.New("systemd description exceeds its size limit")
			}
			result.Description = value
		default:
			return servicesv1.StatusResponse{}, errors.New("systemd returned an unexpected property")
		}
	}
	if err := scanner.Err(); err != nil {
		return servicesv1.StatusResponse{}, errors.New("systemd status response could not be read")
	}
	if !seen["Id"] || !seen["LoadState"] || !seen["ActiveState"] || !seen["SubState"] {
		return servicesv1.StatusResponse{}, errors.New("systemd status response omitted required properties")
	}
	return result, nil
}

func safeExitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("exit code %d", exitErr.ExitCode())
	}
	return errors.New("systemctl could not be executed")
}

// Do not inherit Agent credentials, enrollment tokens or arbitrary process
// settings into the service-manager subprocess.
func serviceManagerEnvironment() []string {
	return []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"SYSTEMD_PAGER=cat",
		"SYSTEMD_COLORS=0",
		"DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket",
	}
}

func (m *systemdServiceManager) serviceControlLock(service string) chan struct{} {
	m.lockMu.Lock()
	defer m.lockMu.Unlock()
	if m.controlLock == nil {
		m.controlLock = make(map[string]chan struct{})
	}
	lock := m.controlLock[service]
	if lock == nil {
		lock = make(chan struct{}, 1)
		m.controlLock[service] = lock
	}
	return lock
}
