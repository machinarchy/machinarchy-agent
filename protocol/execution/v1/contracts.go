// Package executionv1 contains transport-neutral operation contracts shared by
// the Machinarchy controller and execution transports (SSH and Agent).
//
// These contracts describe the requested work and its result. They deliberately
// do not describe how the work is delivered to a host.
package executionv1

const (
	CapabilityCommand = "exec.command"
	CapabilityScript  = "exec.script"

	MaxCommandBytes       = 4096
	MaxArgumentCount      = 256
	MaxArgumentBytes      = 16 * 1024
	MaxEnvironmentEntries = 64
	MaxEnvironmentBytes   = 32 * 1024
	MaxWorkingDirBytes    = 4096
	MaxStdinBytes         = 48 * 1024
	MaxScriptBytes        = 48 * 1024
)

// CommandRequest executes one executable with an argument vector. Command is
// never interpreted by a shell. Use ScriptRequest when shell semantics are
// explicitly required.
type CommandRequest struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
	Stdin            string            `json:"stdin,omitempty"`
}

// ScriptRequest executes Script using the Agent's locally configured shell.
// The controller cannot select an interpreter or inject shell flags.
type ScriptRequest struct {
	Interpreter      string            `json:"interpreter"`
	Script           string            `json:"script"`
	Args             []string          `json:"args,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
}

// Result is the common output shape returned by SSH and Agent executors.
// Stdout and Stderr are UTF-8 text; invalid byte sequences are replaced.
type Result struct {
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	ExitCode        int    `json:"exit_code"`
	DurationMillis  int64  `json:"duration_ms"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
}
