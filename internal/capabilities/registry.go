package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Capability executes one explicitly registered operation. Implementations
// must honor context cancellation and validate the capability-specific input.
type Capability interface {
	Execute(context.Context, json.RawMessage) (json.RawMessage, error)
}

type CapabilityFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (f CapabilityFunc) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return f(ctx, input)
}

// Registry contains the explicitly enabled OS capabilities available to the
// operation dispatcher. Registration is closed before the dispatcher starts.
type Registry struct {
	mu           sync.RWMutex
	capabilities map[string]Capability
	sealed       bool
}

func NewRegistry() *Registry {
	return &Registry{capabilities: make(map[string]Capability)}
}

func (r *Registry) Register(name string, capability Capability) error {
	if r == nil {
		return errors.New("capability registry is required")
	}
	if !ValidName(name) {
		return errors.New("capability name has an invalid format")
	}
	if capability == nil {
		return errors.New("capability implementation is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.New("capability registry is sealed")
	}
	if _, exists := r.capabilities[name]; exists {
		return fmt.Errorf("capability %q is already registered", name)
	}
	r.capabilities[name] = capability
	return nil
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.capabilities))
	for name := range r.capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Lookup(name string) (Capability, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	capability, ok := r.capabilities[name]
	return capability, ok
}

func (r *Registry) Seal() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

func ValidName(name string) bool {
	if name == "" || len(name) > 128 || strings.TrimSpace(name) != name {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
		for _, char := range part {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' {
				return false
			}
		}
	}
	return true
}

// RuntimeInfoCapability exposes a deliberately small, read-only subset of
// process metadata. It excludes environment variables, credentials and
// arbitrary command output.
func RuntimeInfoCapability(agentVersion string) Capability {
	return runtimeInfoCapability(agentVersion, []string{"runtime.info"})
}

func runtimeInfoCapability(agentVersion string, registered []string) Capability {
	return CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if ctx == nil {
			return nil, errors.New("runtime info context is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := requireEmptyObject(input); err != nil {
			return nil, err
		}
		names := append([]string(nil), registered...)
		sort.Strings(names)
		result := struct {
			AgentVersion string   `json:"agent_version"`
			GoVersion    string   `json:"go_version"`
			OS           string   `json:"os"`
			Architecture string   `json:"architecture"`
			CPUCount     int      `json:"cpu_count"`
			Capabilities []string `json:"capabilities"`
		}{AgentVersion: agentVersion, GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, CPUCount: runtime.NumCPU(), Capabilities: names}
		return json.Marshal(result)
	})
}

func requireEmptyObject(raw json.RawMessage) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return errors.New("capability input must be a JSON object")
	}
	if len(value) != 0 {
		return errors.New("capability does not accept input fields")
	}
	return nil
}
