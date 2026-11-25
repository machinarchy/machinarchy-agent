// Package capabilities implements the explicitly enabled, bounded operations
// the agent can perform on its host. There is deliberately no generic command
// or script execution.
package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

// Capability executes one operation. Implementations must validate input,
// honor ctx and return bounded JSON output.
type Capability interface {
	Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// Func adapts a function to Capability.
type Func func(context.Context, json.RawMessage) (json.RawMessage, error)

// Execute calls f.
func (f Func) Execute(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return f(ctx, input)
}

// InvalidInputError marks a rejection caused by the request rather than the
// host, so it can be reported as invalid_input instead of execution_failed.
type InvalidInputError struct{ reason string }

func (e *InvalidInputError) Error() string { return "invalid capability input: " + e.reason }

func invalidInput(format string, args ...any) error {
	return &InvalidInputError{reason: fmt.Sprintf(format, args...)}
}

// Options is the local policy that decides which capabilities exist.
type Options struct {
	AgentVersion   string
	Services       ServiceOptions
	ServiceEnabled bool
}

// Registry is an immutable set of named capabilities.
type Registry struct {
	capabilities map[string]Capability
}

// New builds the registry: runtime.info and system.facts always, and
// service.status / service.control only when enabled by local policy.
func New(options Options) (*Registry, error) {
	capabilities := map[string]Capability{
		agentv1.CapabilitySystemFacts: systemFacts(options.AgentVersion),
	}
	if options.ServiceEnabled {
		status, control, err := newServiceCapabilities(options.Services)
		if err != nil {
			return nil, err
		}
		capabilities[agentv1.CapabilityServiceStatus] = status
		if control != nil {
			capabilities[agentv1.CapabilityServiceControl] = control
		}
	}
	names := make([]string, 0, len(capabilities)+1)
	for name := range capabilities {
		names = append(names, name)
	}
	names = append(names, agentv1.CapabilityRuntimeInfo)
	sort.Strings(names)
	capabilities[agentv1.CapabilityRuntimeInfo] = runtimeInfo(options.AgentVersion, names)
	return &Registry{capabilities: capabilities}, nil
}

// NewStatic builds a registry from explicit implementations. It exists for
// tests of components that consume a registry.
func NewStatic(capabilities map[string]Capability) *Registry {
	copied := make(map[string]Capability, len(capabilities))
	for name, capability := range capabilities {
		copied[name] = capability
	}
	return &Registry{capabilities: copied}
}

// Lookup returns the named capability.
func (r *Registry) Lookup(name string) (Capability, bool) {
	capability, ok := r.capabilities[name]
	return capability, ok
}

// Names returns the sorted capability names.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.capabilities))
	for name := range r.capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runtimeInfo(agentVersion string, names []string) Capability {
	return Func(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if err := requireEmptyObject(input); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return json.Marshal(agentv1.RuntimeInfo{
			AgentVersion: agentVersion, GoVersion: runtime.Version(), OS: runtime.GOOS,
			Architecture: runtime.GOARCH, CPUCount: runtime.NumCPU(), Capabilities: names,
		})
	})
}

func requireEmptyObject(input json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := agentv1.DecodeStrict(input, &fields); err != nil || fields == nil {
		return invalidInput("input must be a JSON object")
	}
	if len(fields) != 0 {
		return invalidInput("this capability takes no input fields")
	}
	return nil
}

func decodeInput(input json.RawMessage, out any) error {
	if len(input) == 0 || len(input) > 4096 {
		return invalidInput("input is empty or too large")
	}
	if err := agentv1.DecodeStrict(input, out); err != nil {
		return invalidInput("input does not match the capability schema")
	}
	return nil
}

// IsInvalidInput reports whether err was caused by the request.
func IsInvalidInput(err error) bool {
	var target *InvalidInputError
	return errors.As(err, &target)
}
