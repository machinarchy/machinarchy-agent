package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	executionv1 "github.com/machinarchy/machinarchy-protocol/execution/v1"
)

func newTestExecutionCapabilities(t *testing.T, maxOutputBytes int) (Capability, Capability) {
	t.Helper()
	command, script, err := NewExecutionCapabilities(ExecutionCapabilityConfig{
		ShellPath: "/bin/sh", MaxOutputBytes: maxOutputBytes,
	})
	if err != nil {
		t.Fatalf("NewExecutionCapabilities() error = %v", err)
	}
	return command, script
}

func TestCommandCapabilityExecutesArgvWithoutShellInterpretation(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{
		Command: "/usr/bin/printf",
		Args:    []string{"%s", "literal; $(touch /tmp/machinarchy-should-not-exist)"},
	}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result executionv1.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode execution result: %v", err)
	}
	if result.Stdout != request.Args[1] {
		t.Fatalf("stdout = %q, want literal argument %q", result.Stdout, request.Args[1])
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestScriptCapabilityRunsConfiguredShellAndPreservesStreams(t *testing.T) {
	_, script := newTestExecutionCapabilities(t, 4096)
	request := executionv1.ScriptRequest{
		Interpreter: "/bin/sh",
		Script:      "printf 'hello %s\\n' \"$1\"; printf 'warning\\n' >&2",
		Args:        []string{"machinarchy"},
	}
	input, _ := json.Marshal(request)
	output, err := script.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result executionv1.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode execution result: %v", err)
	}
	if result.Stdout != "hello machinarchy\n" || result.Stderr != "warning\n" || result.ExitCode != 0 {
		t.Fatalf("unexpected execution result: %#v", result)
	}
}

func TestCommandCapabilityReportsNonZeroExitWithOutput(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'partial-output'; printf 'diagnostic' >&2; exit 7"},
	}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	var executionErr *CapabilityExecutionError
	if !errors.As(err, &executionErr) {
		t.Fatalf("Execute() error = %v, want CapabilityExecutionError", err)
	}
	var result executionv1.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode execution result: %v", err)
	}
	if result.ExitCode != 7 || result.Stdout != "partial-output" || result.Stderr != "diagnostic" {
		t.Fatalf("unexpected execution result: %#v", result)
	}
	if executionErr.Code != "command_exit_nonzero" {
		t.Fatalf("execution error code = %q", executionErr.Code)
	}
}

func TestCommandCapabilityRejectsProtectedEnvironmentVariables(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{
		Command:     "/usr/bin/true",
		Environment: map[string]string{"MACHINARCHY_AGENT_NATS_TOKEN": "must-not-be-injected"},
	}
	input, _ := json.Marshal(request)
	if _, err := command.Execute(context.Background(), input); err == nil {
		t.Fatal("Execute() accepted a protected Machinarchy environment variable")
	}
}

func TestCommandCapabilityDoesNotInheritAgentProcessEnvironment(t *testing.T) {
	const key = "MACHINARCHY_AGENT_TEST_SECRET"
	t.Setenv(key, "agent-secret-value")
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{Command: "/usr/bin/env"}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(string(output), "agent-secret-value") || strings.Contains(string(output), key) {
		t.Fatalf("child process inherited agent environment: %s", output)
	}
}

func TestExecutionCapabilityOutputIsBoundedAndMarkedTruncated(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 1024)
	request := executionv1.CommandRequest{
		Command: "/usr/bin/printf",
		Args:    []string{"%02000d", "0"},
	}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(output) > 1024 {
		t.Fatalf("execution output length = %d, exceeds 1024", len(output))
	}
	var result executionv1.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode execution result: %v", err)
	}
	if !result.StdoutTruncated {
		t.Fatal("StdoutTruncated = false, want true")
	}
}

func TestExecutionCapabilityHonorsContextCancellation(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	request := executionv1.CommandRequest{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 10 & wait"},
	}
	input, _ := json.Marshal(request)
	go func() {
		_, err := command.Execute(ctx, input)
		finished <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-finished:
		var executionErr *CapabilityExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("Execute() error = %v, want interrupted execution error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command process group did not stop after cancellation")
	}
}

func TestExecutionWorkingDirectoryMustBeAbsolute(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{Command: "/usr/bin/true", WorkingDirectory: "relative/path"}
	input, _ := json.Marshal(request)
	if _, err := command.Execute(context.Background(), input); err == nil {
		t.Fatal("Execute() accepted a relative working directory")
	}
}

func TestDefaultCapabilityRegistryOnlyEnablesExecutionWhenConfigured(t *testing.T) {
	config := validTestConfig()
	registry, err := defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry() error = %v", err)
	}
	if got := strings.Join(registry.Names(), ","); got != "runtime.info,system.facts" {
		t.Fatalf("default capabilities = %q, want runtime.info,system.facts", got)
	}

	config.CommandExecutionEnabled = true
	registry, err = defaultCapabilityRegistry("test", config)
	if err != nil {
		t.Fatalf("defaultCapabilityRegistry(enabled) error = %v", err)
	}
	if got := strings.Join(registry.Names(), ","); got != "exec.command,exec.script,runtime.info,system.facts" {
		t.Fatalf("enabled capabilities = %q", got)
	}
}

func TestRuntimeInfoReportsRegisteredCapabilities(t *testing.T) {
	registry, err := defaultCapabilityRegistry("test", validTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	capability, ok := registry.lookup("runtime.info")
	if !ok {
		t.Fatal("runtime.info capability is missing")
	}
	output, err := capability.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Capabilities) != 2 || result.Capabilities[0] != "runtime.info" || result.Capabilities[1] != "system.facts" {
		t.Fatalf("runtime.info capabilities = %#v", result.Capabilities)
	}
}

func TestExecutionChildUsesExplicitWorkingDirectory(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	directory := t.TempDir()
	request := executionv1.CommandRequest{
		Command:          "/bin/pwd",
		WorkingDirectory: directory,
	}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result executionv1.Result
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Stdout) != directory {
		t.Fatalf("pwd output = %q, want %q", result.Stdout, directory)
	}
}

func TestExecutionFailureDoesNotExposeHostErrorDetails(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 4096)
	request := executionv1.CommandRequest{Command: "/definitely/not/a/real/command"}
	input, _ := json.Marshal(request)
	output, err := command.Execute(context.Background(), input)
	var executionErr *CapabilityExecutionError
	if !errors.As(err, &executionErr) {
		t.Fatalf("Execute() error = %v, want CapabilityExecutionError", err)
	}
	if strings.Contains(err.Error(), "/definitely/not/a/real/command") {
		t.Fatalf("execution error leaks command path: %v", err)
	}
	if !json.Valid(output) {
		t.Fatalf("execution error output is not valid JSON: %s", output)
	}
}

func TestExecutionScriptRequiresLocallyAllowedInterpreter(t *testing.T) {
	_, script := newTestExecutionCapabilities(t, 4096)
	input, _ := json.Marshal(executionv1.ScriptRequest{Interpreter: "/bin/bash", Script: "echo should-not-run"})
	if _, err := script.Execute(context.Background(), input); err == nil {
		t.Fatal("script capability accepted an interpreter outside local policy")
	}
}

func TestExecutionScriptRejectsInvalidUTF8AndNUL(t *testing.T) {
	_, script := newTestExecutionCapabilities(t, 4096)
	for _, value := range []string{"", "\x00echo nope", string([]byte{0xff, 0xfe})} {
		input, _ := json.Marshal(executionv1.ScriptRequest{Interpreter: "/bin/sh", Script: value})
		if _, err := script.Execute(context.Background(), input); err == nil {
			t.Fatalf("Execute() accepted invalid script %q", value)
		}
	}
}

func TestExecutionCapabilityDoesNotModifyAgentIdentityOrState(t *testing.T) {
	// A lightweight regression guard: execution only returns output and does
	// not create files unless the requested command itself does so.
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	command, _ := newTestExecutionCapabilities(t, 4096)
	input, _ := json.Marshal(executionv1.CommandRequest{Command: "/bin/true"})
	if _, err := command.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("agent process working directory changed from %q to %q", before, after)
	}
}

func TestDispatcherPublishesNonZeroExitAsFailedWithExecutionOutput(t *testing.T) {
	command, _ := newTestExecutionCapabilities(t, 1024)
	registry := NewCapabilityRegistry()
	if err := registry.Register(executionv1.CapabilityCommand, command); err != nil {
		t.Fatal(err)
	}
	publisher := newMemoryOperationPublisher()
	dispatcher := newTestDispatcher(t, 1, 2, registry, publisher)

	request, _ := json.Marshal(OperationRequestPayload{
		OperationID: "operation-exec-1", Capability: executionv1.CapabilityCommand,
		Input:        json.RawMessage(`{"command":"/bin/sh","args":["-c","printf result-output; exit 9"]}`),
		TimeoutMilli: 5000, OutputLimitBytes: 1024,
	})
	now := time.Now().UTC()
	identity := validTestIdentity()
	event := AgentEventEnvelope{
		ProtocolVersion: agentEventProtocolVersion, EventID: "event-exec-1",
		EventType: AgentEventOperationRequested, TargetAgentID: identity.AgentID,
		TargetHostID: identity.HostID, CorrelationID: "corr-event-exec-1",
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Payload: request,
	}
	if err := dispatcher.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	result := awaitResult(t, publisher, OperationStatusFailed)
	if result.ErrorCode != "command_exit_nonzero" {
		t.Fatalf("ErrorCode = %q, want command_exit_nonzero", result.ErrorCode)
	}
	var executionResult executionv1.Result
	if err := json.Unmarshal(result.Output, &executionResult); err != nil {
		t.Fatalf("decode failed operation output: %v", err)
	}
	if executionResult.ExitCode != 9 || executionResult.Stdout != "result-output" {
		t.Fatalf("unexpected failed operation output: %#v", executionResult)
	}
}
