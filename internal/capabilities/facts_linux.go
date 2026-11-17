//go:build linux

package capabilities

import (
	"strings"
	"syscall"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

func collectPlatformFacts(facts *agentv1.SystemFacts) {
	container := inContainer()
	osRelease := "/etc/os-release"
	if container {
		// The container's own os-release describes the image, not the host.
		osRelease = containerOSRelease
	}
	if data, ok := readSmallFile(osRelease); ok {
		facts.OS.Name, facts.OS.Version = parseOSRelease(data)
	}
	// /proc/sys/kernel/osrelease is the host kernel even inside containers and
	// avoids syscall.Utsname, whose fields are int8 on amd64/arm64 but uint8 on
	// linux/arm and ppc64le (the previous implementation did not compile there).
	if data, ok := readSmallFile("/proc/sys/kernel/osrelease"); ok {
		facts.OS.Kernel = sanitize(strings.TrimSpace(string(data)), 128)
	}
	if data, ok := readSmallFile("/proc/meminfo"); ok {
		facts.Memory = parseMeminfo(data)
	}
	if data, ok := readSmallFile("/proc/uptime"); ok {
		facts.UptimeSeconds = parseUptime(data)
	}
	if !container {
		facts.RootFS = rootFilesystem()
	}
}

func rootFilesystem() *agentv1.CapacityFacts {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil || stat.Bsize <= 0 {
		return nil
	}
	blockSize := uint64(stat.Bsize)
	if stat.Blocks == 0 || stat.Blocks > ^uint64(0)/blockSize || stat.Bavail > stat.Blocks {
		return nil
	}
	return &agentv1.CapacityFacts{TotalBytes: stat.Blocks * blockSize, AvailableBytes: stat.Bavail * blockSize}
}
