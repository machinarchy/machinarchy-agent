//go:build linux

package capabilities

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsContainerEnvironmentAt(t *testing.T) {
	root := t.TempDir()
	if isContainerEnvironmentAt(root) {
		t.Fatal("empty directory must not be detected as a container")
	}
	marker := filepath.Join(root, ".dockerenv")
	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if !isContainerEnvironmentAt(root) {
		t.Fatal("Docker marker was not detected")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run", ".containerenv"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if !isContainerEnvironmentAt(root) {
		t.Fatal("Podman marker was not detected")
	}
}

func TestReadOSReleaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	content := "NAME=Example Linux\nPRETTY_NAME=\"Example Linux 1.2\"\nVERSION_ID=1.2\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	name, version := readOSReleaseFile(path)
	if name != "Example Linux 1.2" || version != "1.2" {
		t.Fatalf("unexpected OS release values: name=%q version=%q", name, version)
	}
}

func TestReadOSReleaseFileMissingReturnsEmpty(t *testing.T) {
	name, version := readOSReleaseFile(filepath.Join(t.TempDir(), "missing"))
	if name != "" || version != "" {
		t.Fatalf("missing host os-release must not return a fallback: name=%q version=%q", name, version)
	}
}

func TestOSReleasePathDependsOnContainerRuntime(t *testing.T) {
	if got := osReleasePath(false); got != "/etc/os-release" {
		t.Fatalf("native OS release path = %q, want /etc/os-release", got)
	}
	if got := osReleasePath(true); got != "/run/machinarchy-host/os-release" {
		t.Fatalf("container OS release path = %q, want /run/machinarchy-host/os-release", got)
	}
}
