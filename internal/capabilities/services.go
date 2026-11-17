package capabilities

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

const (
	maxSystemctlOutput = 8 << 10
	maxAllowedServices = 64
)

// ServiceOptions is the local policy for service capabilities. The controller
// can only choose a unit from Allowed and an action from the protocol's fixed
// set; it can never pass flags, paths or other arguments to systemctl.
type ServiceOptions struct {
	SystemctlPath  string
	Allowed        []string
	ControlEnabled bool
}

type serviceManager interface {
	Status(ctx context.Context, unit string) (agentv1.ServiceStatus, error)
	Control(ctx context.Context, unit, action string) error
}

func newServiceCapabilities(options ServiceOptions) (status, control Capability, err error) {
	if len(options.Allowed) == 0 || len(options.Allowed) > maxAllowedServices {
		return nil, nil, fmt.Errorf("service allowlist must contain between 1 and %d units", maxAllowedServices)
	}
	allowed := make(map[string]struct{}, len(options.Allowed))
	for _, unit := range options.Allowed {
		if err := agentv1.ValidateServiceName(unit); err != nil {
			return nil, nil, fmt.Errorf("service allowlist entry %q: %w", unit, err)
		}
		allowed[unit] = struct{}{}
	}
	if !filepath.IsAbs(options.SystemctlPath) {
		return nil, nil, errors.New("systemctl path must be absolute")
	}
	info, err := os.Stat(options.SystemctlPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, nil, errors.New("systemctl path is not an executable file")
	}
	manager := &systemctl{path: options.SystemctlPath, locks: map[string]*sync.Mutex{}}
	status = serviceStatus(manager, allowed)
	if options.ControlEnabled {
		control = serviceControl(manager, allowed)
	}
	return status, control, nil
}

func checkAllowed(unit string, allowed map[string]struct{}) error {
	if err := agentv1.ValidateServiceName(unit); err != nil {
		return invalidInput("%v", err)
	}
	if _, ok := allowed[unit]; !ok {
		return invalidInput("service is not in the local allowlist")
	}
	return nil
}

func serviceStatus(manager serviceManager, allowed map[string]struct{}) Capability {
	return Func(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		var request agentv1.ServiceStatusRequest
		if err := decodeInput(input, &request); err != nil {
			return nil, err
		}
		if err := checkAllowed(request.Service, allowed); err != nil {
			return nil, err
		}
		status, err := manager.Status(ctx, request.Service)
		if err != nil {
			return nil, err
		}
		return json.Marshal(status)
	})
}

func serviceControl(manager serviceManager, allowed map[string]struct{}) Capability {
	return Func(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		var request agentv1.ServiceControlRequest
		if err := decodeInput(input, &request); err != nil {
			return nil, err
		}
		if err := checkAllowed(request.Service, allowed); err != nil {
			return nil, err
		}
		if err := agentv1.ValidateServiceAction(request.Action); err != nil {
			return nil, invalidInput("%v", err)
		}
		if err := manager.Control(ctx, request.Service, request.Action); err != nil {
			return nil, err
		}
		return json.Marshal(agentv1.ServiceControlResponse{Service: request.Service, Action: request.Action, Result: "completed"})
	})
}

// systemctl drives systemd through the systemctl binary with a fixed argument
// layout, an empty environment and bounded output. Privilege comes only from
// the host's polkit policy; the agent never elevates.
type systemctl struct {
	path  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (s *systemctl) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, s.path, args...) // #nosec G204 -- fixed binary; args are validated unit names and fixed verbs.
	stdout := &boundedBuffer{limit: maxSystemctlOutput}
	cmd.Stdout, cmd.Stderr = stdout, &boundedBuffer{limit: 1024}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "SYSTEMD_PAGER=", "SYSTEMD_COLORS=0"}
	cmd.Dir = "/"
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("systemctl exited with status %d", exitErr.ExitCode())
		}
		return "", errors.New("systemctl could not be started")
	}
	if stdout.truncated {
		return "", errors.New("systemctl output exceeded its limit")
	}
	return stdout.String(), nil
}

// Status reads unit properties. "--" ends option parsing, so a unit name can
// never be interpreted as a flag.
func (s *systemctl) Status(ctx context.Context, unit string) (agentv1.ServiceStatus, error) {
	out, err := s.run(ctx, "show", "--no-pager",
		"--property=Id,LoadState,ActiveState,SubState,UnitFileState,Description", "--", unit)
	if err != nil {
		return agentv1.ServiceStatus{}, err
	}
	return parseShow(unit, out)
}

// Control applies one lifecycle action. Actions on the same unit are
// serialized so concurrent operations cannot interleave.
func (s *systemctl) Control(ctx context.Context, unit, action string) error {
	if err := agentv1.ValidateServiceAction(action); err != nil {
		return err
	}
	lock := s.unitLock(unit)
	lock.Lock()
	defer lock.Unlock()
	_, err := s.run(ctx, "--no-ask-password", "--", action, unit)
	return err
}

func (s *systemctl) unitLock(unit string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.locks[unit]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[unit] = lock
	}
	return lock
}

func parseShow(unit, out string) (agentv1.ServiceStatus, error) {
	status := agentv1.ServiceStatus{Service: unit}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok || seen[key] {
			return agentv1.ServiceStatus{}, errors.New("systemctl show output is malformed")
		}
		seen[key] = true
		switch key {
		case "Id":
			if value != unit {
				return agentv1.ServiceStatus{}, errors.New("systemctl reported a different unit")
			}
		case "LoadState":
			status.LoadState = sanitize(value, 64)
		case "ActiveState":
			status.ActiveState = sanitize(value, 64)
		case "SubState":
			status.SubState = sanitize(value, 64)
		case "UnitFileState":
			status.UnitFileState = sanitize(value, 64)
		case "Description":
			status.Description = sanitize(value, 512)
		default:
			return agentv1.ServiceStatus{}, errors.New("systemctl reported an unexpected property")
		}
	}
	if !seen["Id"] || !seen["LoadState"] || !seen["ActiveState"] || !seen["SubState"] {
		return agentv1.ServiceStatus{}, errors.New("systemctl show output is incomplete")
	}
	return status, nil
}

// boundedBuffer keeps at most limit bytes and keeps draining the writer.
type boundedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
			b.truncated = true
		} else {
			b.Buffer.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}
