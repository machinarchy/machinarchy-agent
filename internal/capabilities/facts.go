package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	factsv1 "github.com/machinarchy/machinarchy-protocol/hostfacts/v1"
)

const maxFactsOutputBytes = factsv1.MaxFactsPayloadBytes

// The facts DTOs are aliases to the shared protocol contract so Agent output
// and Controller ingestion cannot silently drift apart.
type SystemFacts = factsv1.SystemFacts
type OperatingSystem = factsv1.OperatingSystem
type CPUFacts = factsv1.CPUFacts
type MemoryFacts = factsv1.MemoryFacts
type FilesystemFacts = factsv1.FilesystemFacts
type AgentRuntimeFacts = factsv1.AgentRuntimeFacts

// SystemFactsCapability returns a read-only snapshot of selected host facts.
// Platform-specific collection is best-effort: unavailable metrics are omitted
// rather than making the entire capability fail.
func SystemFactsCapability(agentVersion string) Capability {
	return CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if ctx == nil {
			return nil, errors.New("system facts context is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := requireEmptyObject(input); err != nil {
			return nil, err
		}
		facts, err := CollectSystemFacts(ctx, agentVersion, time.Now())
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(facts)
		if err != nil {
			return nil, fmt.Errorf("encode system facts: %w", err)
		}
		if len(encoded) > maxFactsOutputBytes {
			return nil, errors.New("system facts output exceeds the maximum size")
		}
		return encoded, nil
	})
}

// CollectSystemFacts collects only explicitly selected, low-sensitivity facts.
// The caller supplies the timestamp to make tests deterministic.
func CollectSystemFacts(ctx context.Context, agentVersion string, collectedAt time.Time) (SystemFacts, error) {
	if ctx == nil {
		return SystemFacts{}, errors.New("system facts context is required")
	}
	if err := ctx.Err(); err != nil {
		return SystemFacts{}, err
	}
	hostname, _ := os.Hostname()
	facts := SystemFacts{
		SchemaVersion: factsv1.SchemaVersion,
		CollectedAt:   collectedAt.UTC(),
		Hostname:      sanitizeFact(hostname, 255),
		OS: OperatingSystem{
			Platform: runtime.GOOS,
		},
		Architecture: runtime.GOARCH,
		CPU:          CPUFacts{LogicalCores: runtime.NumCPU()},
		Agent: AgentRuntimeFacts{
			Version:   sanitizeFact(agentVersion, 128),
			GoVersion: runtime.Version(),
		},
	}
	platformFacts, err := collectPlatformFacts(ctx)
	if err != nil {
		return SystemFacts{}, err
	}
	facts.OS.Name = sanitizeFact(platformFacts.OSName, 128)
	facts.OS.Version = sanitizeFact(platformFacts.OSVersion, 64)
	facts.OS.Kernel = sanitizeFact(platformFacts.Kernel, 128)
	facts.Memory = platformFacts.Memory
	facts.UptimeSeconds = platformFacts.UptimeSeconds
	facts.RootFS = platformFacts.RootFS
	if err := factsv1.Validate(facts); err != nil {
		return SystemFacts{}, fmt.Errorf("validate collected system facts: %w", err)
	}
	return facts, nil
}

func sanitizeFact(value string, max int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > max {
		return value[:max]
	}
	return value
}

type platformFacts struct {
	OSName        string
	OSVersion     string
	Kernel        string
	Memory        *MemoryFacts
	UptimeSeconds *uint64
	RootFS        *FilesystemFacts
}
