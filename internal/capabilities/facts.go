package capabilities

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

const (
	maxFactsBytes      = 16 << 10
	maxProcFileBytes   = 64 << 10
	containerOSRelease = "/run/machinarchy-host/os-release"
)

func systemFacts(agentVersion string) Capability {
	return Func(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		if err := requireEmptyObject(input); err != nil {
			return nil, err
		}
		facts, err := CollectSystemFacts(ctx, agentVersion, time.Now())
		if err != nil {
			return nil, err
		}
		return json.Marshal(facts)
	})
}

// CollectSystemFacts gathers a small, non-sensitive description of the host.
// Values the platform cannot report are omitted rather than guessed.
func CollectSystemFacts(ctx context.Context, agentVersion string, now time.Time) (agentv1.SystemFacts, error) {
	if err := ctx.Err(); err != nil {
		return agentv1.SystemFacts{}, err
	}
	hostname, _ := os.Hostname()
	facts := agentv1.SystemFacts{
		CollectedAt:  now.UTC(),
		Hostname:     sanitize(hostname, 255),
		OS:           agentv1.OperatingSystem{Platform: runtime.GOOS},
		Architecture: runtime.GOARCH,
		LogicalCPUs:  runtime.NumCPU(),
		Agent:        agentv1.AgentRuntimeInfo{Version: sanitize(agentVersion, 64), GoVersion: runtime.Version()},
	}
	collectPlatformFacts(&facts)
	if err := ctx.Err(); err != nil {
		return agentv1.SystemFacts{}, err
	}
	return facts, nil
}

func sanitize(value string, maxLength int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > maxLength {
		value = value[:maxLength]
	}
	return value
}

func readSmallFile(path string) ([]byte, bool) {
	file, err := os.Open(path) // #nosec G304 -- fixed, non-user-controlled paths.
	if err != nil {
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProcFileBytes))
	return data, err == nil
}

// parseOSRelease extracts PRETTY_NAME and VERSION_ID from os-release(5).
func parseOSRelease(data []byte) (name, version string) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "PRETTY_NAME":
			name = sanitize(value, 128)
		case "VERSION_ID":
			version = sanitize(value, 64)
		}
	}
	return name, version
}

// parseMeminfo extracts MemTotal and MemAvailable from /proc/meminfo.
func parseMeminfo(data []byte) *agentv1.CapacityFacts {
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > math.MaxUint64/1024 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	if total == 0 || available > total {
		return nil
	}
	return &agentv1.CapacityFacts{TotalBytes: total, AvailableBytes: available}
}

// parseUptime extracts whole seconds from /proc/uptime.
func parseUptime(data []byte) *uint64 {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(seconds) || seconds < 0 || seconds >= math.MaxUint64 {
		return nil
	}
	value := uint64(seconds)
	return &value
}

func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if info, err := os.Stat(marker); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
