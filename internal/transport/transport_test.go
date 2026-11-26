package transport

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseServerURL(t *testing.T) {
	good := map[string]struct {
		allowPlain bool
		target     string
		tls        bool
	}{
		"https://controller.example.com":      {false, "controller.example.com:443", true},
		"https://controller.example.com:8443": {false, "controller.example.com:8443", true},
		"https://controller.example.com/":     {false, "controller.example.com:443", true},
		"http://127.0.0.1:9000":               {true, "127.0.0.1:9000", false},
	}
	for raw, want := range good {
		target, useTLS, err := ParseServerURL(raw, want.allowPlain)
		if err != nil || target != want.target || useTLS != want.tls {
			t.Errorf("ParseServerURL(%q) = %q, %v, %v", raw, target, useTLS, err)
		}
	}
	for _, raw := range []string{
		"", "controller.example.com", "http://controller:80", "nats://h:4222", "https://u:p@h", "https://h/path", "https://h?x=1", "https://h#f", "https://",
	} {
		if _, _, err := ParseServerURL(raw, false); err == nil {
			t.Errorf("ParseServerURL(%q) accepted", raw)
		}
	}
}

func TestErrorClassifiesRejections(t *testing.T) {
	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied} {
		var rejected RejectedError
		if err := Error(status.Error(code, "x")); !errors.As(err, &rejected) {
			t.Errorf("%s not classified as rejected: %v", code, err)
		}
	}
	var rejected RejectedError
	if err := Error(status.Error(codes.Unavailable, "x")); errors.As(err, &rejected) {
		t.Error("Unavailable classified as rejected")
	}
	if Error(nil) != nil {
		t.Error("nil error changed")
	}
}
