package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	executionv1 "github.com/machinarchy/machinarchy-protocol/execution/v1"
)

const (
	defaultExecutionPATH         = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	minimumExecutionOutputBytes  = 1024
	maxExecutionEnvironmentBytes = executionv1.MaxEnvironmentBytes
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ExecutionCapabilityConfig controls the local command execution boundary.
// The controller cannot override the configured shell or output limit.
type ExecutionCapabilityConfig struct {
	ShellPath      string
	MaxOutputBytes int
}

// CapabilityExecutionError carries a normalized execution result while
// allowing the dispatcher to report a failed, timed-out, or cancelled job.
// The underlying OS error is intentionally not exposed in Error() or logs.
type CapabilityExecutionError struct {
	Code   string
	Output json.RawMessage
	cause  error
}

func (e *CapabilityExecutionError) Error() string {
	return "remote execution failed"
}

func (e *CapabilityExecutionError) Unwrap() error {
	return e.cause
}

// NewExecutionCapabilities creates the two transport-neutral execution
// capabilities. They are registered only when local command execution has
// been explicitly enabled in Agent configuration.
func NewExecutionCapabilities(config ExecutionCapabilityConfig) (Capability, Capability, error) {
	if config.MaxOutputBytes < minimumExecutionOutputBytes || config.MaxOutputBytes > maxOperationOutputBytes {
		return nil, nil, fmt.Errorf("execution output limit must be between %d and %d bytes", minimumExecutionOutputBytes, maxOperationOutputBytes)
	}
	if config.ShellPath == "" || !filepath.IsAbs(config.ShellPath) || strings.ContainsAny(config.ShellPath, "\r\n\x00") {
		return nil, nil, errors.New("execution shell path must be an absolute single-line path")
	}
	if _, err := validateExecutable(config.ShellPath); err != nil {
		return nil, nil, errors.New("configured execution shell is not an executable file")
	}

	command := CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		var request executionv1.CommandRequest
		if err := decodeStrictObject(input, &request, maxOperationInputBytes); err != nil {
			return nil, errors.New("command input is invalid")
		}
		if err := validateCommandRequest(request); err != nil {
			return nil, err
		}
		executable, err := resolveExecutionCommand(request.Command)
		if err != nil {
			return executionFailure(config.MaxOutputBytes, "command_not_found", err, -1)
		}
		return runExecution(ctx, executable, request.Args, request.Stdin, request.WorkingDirectory, request.Environment, executionOutputLimit(ctx, config.MaxOutputBytes), "command")
	})

	script := CapabilityFunc(func(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
		var request executionv1.ScriptRequest
		if err := decodeStrictObject(input, &request, maxOperationInputBytes); err != nil {
			return nil, errors.New("script input is invalid")
		}
		if err := validateScriptRequest(request, config.ShellPath); err != nil {
			return nil, err
		}
		// The interpreter is explicit in the transport-neutral request but must
		// match the local Agent allowlist. Script text is supplied on stdin; it
		// is never interpolated into a shell -c argument.
		args := []string{"-s", "--"}
		args = append(args, request.Args...)
		return runExecution(ctx, config.ShellPath, args, request.Script, request.WorkingDirectory, request.Environment, executionOutputLimit(ctx, config.MaxOutputBytes), "script")
	})

	return command, script, nil
}

func validateCommandRequest(request executionv1.CommandRequest) error {
	if request.Command == "" || strings.TrimSpace(request.Command) != request.Command || len(request.Command) > executionv1.MaxCommandBytes || strings.ContainsAny(request.Command, "\r\n\x00") {
		return errors.New("command must be a non-empty executable name or absolute path")
	}
	if len(request.Args) > executionv1.MaxArgumentCount {
		return errors.New("command has too many arguments")
	}
	for _, arg := range request.Args {
		if len(arg) > executionv1.MaxArgumentBytes || strings.ContainsRune(arg, '\x00') {
			return errors.New("command argument is invalid or too large")
		}
	}
	if len(request.Stdin) > executionv1.MaxStdinBytes || strings.ContainsRune(request.Stdin, '\x00') {
		return errors.New("command stdin is invalid or too large")
	}
	return validateExecutionOptions(request.WorkingDirectory, request.Environment)
}

func validateScriptRequest(request executionv1.ScriptRequest, allowedInterpreter string) error {
	if request.Interpreter == "" || !filepath.IsAbs(request.Interpreter) || filepath.Clean(request.Interpreter) != filepath.Clean(allowedInterpreter) {
		return errors.New("script interpreter is not permitted by the local Agent policy")
	}
	if request.Script == "" || len(request.Script) > executionv1.MaxScriptBytes || !utf8.ValidString(request.Script) || strings.ContainsRune(request.Script, '\x00') {
		return errors.New("script is empty, invalid UTF-8, or too large")
	}
	if len(request.Args) > executionv1.MaxArgumentCount {
		return errors.New("script has too many arguments")
	}
	for _, arg := range request.Args {
		if len(arg) > executionv1.MaxArgumentBytes || strings.ContainsRune(arg, '\x00') {
			return errors.New("script argument is invalid or too large")
		}
	}
	return validateExecutionOptions(request.WorkingDirectory, request.Environment)
}

func validateExecutionOptions(workingDirectory string, environment map[string]string) error {
	if len(workingDirectory) > executionv1.MaxWorkingDirBytes || strings.ContainsAny(workingDirectory, "\r\n\x00") {
		return errors.New("working directory is invalid or too large")
	}
	if workingDirectory != "" && !filepath.IsAbs(workingDirectory) {
		return errors.New("working directory must be an absolute path")
	}
	if len(environment) > executionv1.MaxEnvironmentEntries {
		return errors.New("execution environment contains too many variables")
	}
	totalBytes := 0
	for name, value := range environment {
		if !environmentNamePattern.MatchString(name) || strings.HasPrefix(strings.ToUpper(name), "MACHINARCHY_") || strings.HasPrefix(strings.ToUpper(name), "LD_") || strings.HasPrefix(strings.ToUpper(name), "DYLD_") {
			return errors.New("execution environment contains a reserved or invalid variable name")
		}
		switch strings.ToUpper(name) {
		case "PATH", "HOME", "IFS", "ENV", "BASH_ENV", "SHELLOPTS":
			return errors.New("execution environment attempts to override a protected variable")
		}
		if strings.ContainsRune(value, '\x00') {
			return errors.New("execution environment contains a NUL byte")
		}
		totalBytes += len(name) + len(value) + 2
		if totalBytes > maxExecutionEnvironmentBytes {
			return errors.New("execution environment exceeds the configured input limit")
		}
	}
	return nil
}

func resolveExecutionCommand(command string) (string, error) {
	if filepath.IsAbs(command) {
		return validateExecutable(command)
	}
	if strings.ContainsAny(command, `/\\`) || command == "." || command == ".." {
		return "", errors.New("relative executable paths are not allowed; use an absolute path or executable name")
	}
	for _, directory := range strings.Split(defaultExecutionPATH, string(os.PathListSeparator)) {
		candidate := filepath.Join(directory, command)
		if resolved, err := validateExecutable(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("executable was not found in the Agent execution PATH")
}

func validateExecutable(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("executable path is not a regular executable file")
	}
	return filepath.Clean(path), nil
}

type executionOutputLimitKey struct{}

func executionOutputLimit(ctx context.Context, fallback int) int {
	if ctx == nil {
		return fallback
	}
	if value, ok := ctx.Value(executionOutputLimitKey{}).(int); ok && value >= minimumExecutionOutputBytes && value < fallback {
		return value
	}
	return fallback
}

func runExecution(ctx context.Context, executable string, args []string, stdin, workingDirectory string, environment map[string]string, maxOutputBytes int, kind string) (json.RawMessage, error) {
	if ctx == nil {
		return nil, errors.New("execution context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	stdout := &limitedOutputBuffer{limit: maxOutputBytes / 2}
	stderr := &limitedOutputBuffer{limit: maxOutputBytes / 2}
	cmd := exec.CommandContext(ctx, executable, args...)
	configureCommandCancellation(cmd)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = buildExecutionEnvironment(environment)
	if workingDirectory == "" {
		cmd.Dir = string(filepath.Separator)
	} else {
		cmd.Dir = workingDirectory
	}

	started := time.Now()
	runErr := cmd.Run()
	completed := time.Now()
	result := executionv1.Result{
		Stdout:          strings.ToValidUTF8(stdout.String(), "\uFFFD"),
		Stderr:          strings.ToValidUTF8(stderr.String(), "\uFFFD"),
		ExitCode:        0,
		DurationMillis:  completed.Sub(started).Milliseconds(),
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}
	if runErr != nil {
		result.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}

	output, marshalErr := marshalBoundedExecutionResult(result, maxOutputBytes)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode execution result: %w", marshalErr)
	}
	if ctx.Err() != nil {
		return output, &CapabilityExecutionError{Code: "execution_interrupted", Output: output, cause: ctx.Err()}
	}
	if runErr != nil {
		code := kind + "_start_failed"
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = kind + "_exit_nonzero"
		}
		return output, &CapabilityExecutionError{Code: code, Output: output, cause: runErr}
	}
	return output, nil
}

func executionFailure(maxOutputBytes int, code string, cause error, exitCode int) (json.RawMessage, error) {
	output, err := marshalBoundedExecutionResult(executionv1.Result{ExitCode: exitCode}, maxOutputBytes)
	if err != nil {
		return nil, err
	}
	return output, &CapabilityExecutionError{Code: code, Output: output, cause: cause}
}

func marshalBoundedExecutionResult(result executionv1.Result, maxBytes int) (json.RawMessage, error) {
	for {
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxBytes {
			return encoded, nil
		}

		if len(result.Stdout) == 0 && len(result.Stderr) == 0 {
			return nil, errors.New("execution result metadata exceeds the configured output limit")
		}
		overflow := len(encoded) - maxBytes + 1
		if len(result.Stdout) >= len(result.Stderr) && len(result.Stdout) > 0 {
			result.StdoutTruncated = true
			result.Stdout = truncateUTF8(result.Stdout, len(result.Stdout)-overflow)
		} else if len(result.Stderr) > 0 {
			result.StderrTruncated = true
			result.Stderr = truncateUTF8(result.Stderr, len(result.Stderr)-overflow)
		}
	}
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if maxBytes >= len(value) {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func buildExecutionEnvironment(requested map[string]string) []string {
	values := map[string]string{
		"PATH":   defaultExecutionPATH,
		"LANG":   "C",
		"LC_ALL": "C",
		"TMPDIR": os.TempDir(),
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		values["HOME"] = home
	}
	for key, value := range requested {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

type limitedOutputBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedOutputBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = b.buffer.Write(value[:remaining])
	}
	if originalLength > remaining {
		b.truncated = true
	}
	// Continue draining the child process after the capture limit is reached.
	return originalLength, nil
}

func (b *limitedOutputBuffer) String() string {
	return b.buffer.String()
}
