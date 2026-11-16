// Package hostfactsv1 defines the versioned, privacy-conscious host inventory
// snapshot shared by Machinarchy Agents and Controllers.
package hostfactsv1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion        = "v1"
	MaxFactsPayloadBytes = 16 * 1024
	maxLogicalCPUs       = 65536
)

// SystemFacts is a deliberately small host snapshot. Do not add credentials,
// IP addresses, usernames, environment variables, process lists, mount lists,
// machine identifiers, or arbitrary file contents to this contract.
type SystemFacts struct {
	SchemaVersion string            `json:"schema_version"`
	CollectedAt   time.Time         `json:"collected_at"`
	Hostname      string            `json:"hostname,omitempty"`
	OS            OperatingSystem   `json:"os"`
	Architecture  string            `json:"architecture"`
	CPU           CPUFacts          `json:"cpu"`
	Memory        *MemoryFacts      `json:"memory,omitempty"`
	UptimeSeconds *uint64           `json:"uptime_seconds,omitempty"`
	RootFS        *FilesystemFacts  `json:"root_filesystem,omitempty"`
	Agent         AgentRuntimeFacts `json:"agent"`
}

type OperatingSystem struct {
	Platform string `json:"platform"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
}

type CPUFacts struct {
	LogicalCores int `json:"logical_cores"`
}

type MemoryFacts struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type FilesystemFacts struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type AgentRuntimeFacts struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
}

// Decode validates size, JSON shape, version and field bounds before facts are
// admitted into controller inventory.
func Decode(raw []byte) (SystemFacts, error) {
	if len(raw) == 0 || len(raw) > MaxFactsPayloadBytes {
		return SystemFacts{}, errors.New("host facts payload is empty or exceeds the maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var facts SystemFacts
	if err := decoder.Decode(&facts); err != nil {
		return SystemFacts{}, fmt.Errorf("decode host facts: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return SystemFacts{}, errors.New("host facts payload must contain exactly one JSON value")
		}
		return SystemFacts{}, fmt.Errorf("decode trailing host facts data: %w", err)
	}
	if err := Validate(facts); err != nil {
		return SystemFacts{}, err
	}
	return facts, nil
}

func Validate(facts SystemFacts) error {
	if facts.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported host facts schema version %q", facts.SchemaVersion)
	}
	if facts.CollectedAt.IsZero() {
		return errors.New("host facts collected_at is required")
	}
	if err := validateText("hostname", facts.Hostname, 255, false); err != nil {
		return err
	}
	if err := validateText("os.platform", facts.OS.Platform, 32, true); err != nil {
		return err
	}
	if err := validateText("os.name", facts.OS.Name, 128, false); err != nil {
		return err
	}
	if err := validateText("os.version", facts.OS.Version, 64, false); err != nil {
		return err
	}
	if err := validateText("os.kernel", facts.OS.Kernel, 128, false); err != nil {
		return err
	}
	if err := validateText("architecture", facts.Architecture, 32, true); err != nil {
		return err
	}
	if facts.CPU.LogicalCores < 1 || facts.CPU.LogicalCores > maxLogicalCPUs {
		return fmt.Errorf("cpu.logical_cores must be between 1 and %d", maxLogicalCPUs)
	}
	if facts.Memory != nil {
		if facts.Memory.TotalBytes == 0 || facts.Memory.AvailableBytes > facts.Memory.TotalBytes {
			return errors.New("memory byte counts are inconsistent")
		}
	}
	if facts.RootFS != nil {
		if facts.RootFS.TotalBytes == 0 || facts.RootFS.AvailableBytes > facts.RootFS.TotalBytes {
			return errors.New("root filesystem byte counts are inconsistent")
		}
	}
	if err := validateText("agent.version", facts.Agent.Version, 128, false); err != nil {
		return err
	}
	if err := validateText("agent.go_version", facts.Agent.GoVersion, 64, true); err != nil {
		return err
	}
	return nil
}

func validateText(field, value string, maxBytes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}
