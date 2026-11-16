package capabilities

import (
	"bytes"
	"encoding/json"
)

// CapabilityError carries a bounded, normalized result when a capability
// fails after producing useful output. The underlying error is intentionally
// not exposed in Error() or logs.
type CapabilityError struct {
	Code   string
	Output json.RawMessage
	cause  error
}

func (e *CapabilityError) Error() string {
	return "capability execution failed"
}

func (e *CapabilityError) Unwrap() error {
	return e.cause
}

// limitedOutputBuffer retains at most limit bytes while continuing to drain
// the writer. This is used by bounded local OS adapters such as systemctl.
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
	return originalLength, nil
}

func (b *limitedOutputBuffer) String() string {
	return b.buffer.String()
}
