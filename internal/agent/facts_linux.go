//go:build linux

package agent

import (
	"bufio"
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const maxOSReleaseBytes = 16 * 1024

func collectPlatformFacts(ctx context.Context) (platformFacts, error) {
	if err := ctx.Err(); err != nil {
		return platformFacts{}, err
	}
	facts := platformFacts{}
	facts.OSName, facts.OSVersion = readOSRelease()
	facts.Kernel = readKernelRelease()
	facts.Memory = readLinuxMemory()
	facts.UptimeSeconds = readLinuxUptime()
	facts.RootFS = readRootFilesystem()
	if err := ctx.Err(); err != nil {
		return platformFacts{}, err
	}
	return facts, nil
}

func readOSRelease() (name, version string) {
	return readOSReleaseFile(osReleasePath(isContainerEnvironmentAt("/")))
}

func osReleasePath(inContainer bool) string {
	if inContainer {
		// Docker mounts the host release file at a dedicated path rather than
		// over /etc/os-release, which is commonly a symlink in Linux images.
		// If the host file is unavailable, omit the distribution fields instead
		// of silently reporting the container image's distribution.
		return "/run/machinarchy-host/os-release"
	}
	return "/etc/os-release"
}

func readOSReleaseFile(path string) (name, version string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer file.Close()
	limited := bufio.NewScanner(io.LimitReader(file, maxOSReleaseBytes))
	limited.Buffer(make([]byte, 1024), maxOSReleaseBytes)
	for limited.Scan() {
		line := strings.TrimSpace(limited.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, "\"'")
		switch key {
		case "PRETTY_NAME":
			name = value
		case "VERSION_ID":
			version = value
		}
	}
	return name, version
}

func readKernelRelease() string {
	var value syscall.Utsname
	if err := syscall.Uname(&value); err != nil {
		return ""
	}
	return charsToString(value.Release[:])
}

func charsToString(value []int8) string {
	bytes := make([]byte, 0, len(value))
	for _, char := range value {
		if char == 0 {
			break
		}
		bytes = append(bytes, byte(char))
	}
	return string(bytes)
}

func readLinuxMemory() *MemoryFacts {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil
	}
	defer file.Close()
	var total, available uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total = value * 1024
		case "MemAvailable":
			available = value * 1024
		}
	}
	if scanner.Err() != nil || total == 0 || available > total {
		return nil
	}
	return &MemoryFacts{TotalBytes: total, AvailableBytes: available}
}

func readLinuxUptime() *uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil || len(data) > 128 {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > float64(^uint64(0)) {
		return nil
	}
	value := uint64(seconds)
	return &value
}

func readRootFilesystem() *FilesystemFacts {
	// A container's / is its writable overlay or container root, not the host
	// root filesystem. Docker and Podman provide marker files; omit this metric
	// instead of returning a misleading container filesystem measurement.
	if isContainerEnvironmentAt("/") {
		return nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return nil
	}
	if stat.Bsize <= 0 {
		return nil
	}
	blockSize := uint64(stat.Bsize)
	if stat.Blocks > ^uint64(0)/blockSize || stat.Bavail > ^uint64(0)/blockSize {
		return nil
	}
	total := stat.Blocks * blockSize
	available := stat.Bavail * blockSize
	if total == 0 || available > total {
		return nil
	}
	return &FilesystemFacts{TotalBytes: total, AvailableBytes: available}
}

// isContainerEnvironmentAt is separated from readRootFilesystem so container
// detection can be tested without requiring a Docker runtime.
func isContainerEnvironmentAt(root string) bool {
	for _, marker := range []string{".dockerenv", "run/.containerenv"} {
		info, err := os.Stat(filepath.Join(root, marker))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
