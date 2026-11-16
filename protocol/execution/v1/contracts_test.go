package executionv1

import (
	"encoding/json"
	"testing"
)

func TestCommandAndScriptContractsUseStableJSONNames(t *testing.T) {
	command := CommandRequest{
		Command:          "/usr/bin/uptime",
		Args:             []string{"-p"},
		WorkingDirectory: "/",
		Environment:      map[string]string{"LANG": "C"},
		Stdin:            "",
	}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"command", "args", "working_directory", "environment"} {
		if _, exists := fields[key]; !exists {
			t.Fatalf("command contract missing %q: %s", key, body)
		}
	}

	script := ScriptRequest{Interpreter: "/bin/sh", Script: "echo hello"}
	body, err = json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"interpreter", "script"} {
		if _, exists := fields[key]; !exists {
			t.Fatalf("script contract missing %q: %s", key, body)
		}
	}
}

func TestCapabilityNamesAreTransportNeutral(t *testing.T) {
	if CapabilityCommand != "exec.command" || CapabilityScript != "exec.script" {
		t.Fatalf("unexpected capability names: %q %q", CapabilityCommand, CapabilityScript)
	}
}
