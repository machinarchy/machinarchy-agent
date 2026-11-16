package hostfactsv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validFacts() SystemFacts {
	return SystemFacts{
		SchemaVersion: SchemaVersion,
		CollectedAt:   time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC),
		Hostname:      "worker-01",
		OS:            OperatingSystem{Platform: "linux", Name: "Debian GNU/Linux", Version: "13", Kernel: "6.12.0"},
		Architecture:  "amd64",
		CPU:           CPUFacts{LogicalCores: 8},
		Memory:        &MemoryFacts{TotalBytes: 16 << 30, AvailableBytes: 8 << 30},
		Agent:         AgentRuntimeFacts{Version: "1.2.3", GoVersion: "go1.26.0"},
	}
}

func TestDecodeAcceptsValidFacts(t *testing.T) {
	body, err := json.Marshal(validFacts())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hostname != "worker-01" || got.CPU.LogicalCores != 8 {
		t.Fatalf("unexpected facts: %+v", got)
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	body, _ := json.Marshal(validFacts())
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	fields["environment"] = map[string]string{"SECRET": "value"}
	unknown, _ := json.Marshal(fields)
	if _, err := Decode(unknown); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if _, err := Decode(append(body, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestValidateRejectsUnsafeOrInconsistentValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemFacts)
	}{
		{"wrong schema", func(f *SystemFacts) { f.SchemaVersion = "v2" }},
		{"zero cores", func(f *SystemFacts) { f.CPU.LogicalCores = 0 }},
		{"memory overrun", func(f *SystemFacts) { f.Memory.AvailableBytes = f.Memory.TotalBytes + 1 }},
		{"control character", func(f *SystemFacts) { f.Hostname = "bad\nname" }},
		{"missing platform", func(f *SystemFacts) { f.OS.Platform = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := validFacts()
			tt.mutate(&f)
			if err := Validate(f); err == nil {
				t.Fatal("invalid facts were accepted")
			}
		})
	}
}

func TestDecodeEnforcesMaximumPayload(t *testing.T) {
	if _, err := Decode([]byte(strings.Repeat("x", MaxFactsPayloadBytes+1))); err == nil {
		t.Fatal("oversized payload was accepted")
	}
}
