package tun

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestClassifyCopyError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		abnormal bool
		relay    bool
	}{
		{"clean close", nil, false, false},
		{"eof", io.EOF, false, false},
		{"session torn down", net.ErrClosed, false, false},
		{"tagged closed conn from teardown", tagRelayError(net.ErrClosed), false, false},
		{"client reset (app went away)", syscall.ECONNRESET, false, false},
		{"client broken pipe", syscall.EPIPE, false, false},
		{"relay reset mid-stream", tagRelayError(syscall.ECONNRESET), true, true},
		{"relay broken pipe", tagRelayError(syscall.EPIPE), true, true},
		{"relay reset wrapped in op error", tagRelayError(fmt.Errorf("read tcp: %w", syscall.ECONNRESET)), true, true},
		{"relay generic error", tagRelayError(errors.New("tls: bad record")), true, true},
		{"client generic error", errors.New("short write"), true, false},
	}
	for _, tc := range cases {
		abnormal, relay := classifyCopyError(tc.err)
		if abnormal != tc.abnormal || relay != tc.relay {
			t.Errorf("%s: classifyCopyError() = (%v, %v), want (%v, %v)", tc.name, abnormal, relay, tc.abnormal, tc.relay)
		}
	}
}

func TestTagRelayErrorPreservesEOFForIOCopy(t *testing.T) {
	if err := tagRelayError(io.EOF); err != io.EOF {
		t.Fatalf("tagRelayError(io.EOF) = %v, want untouched io.EOF", err)
	}
	if err := tagRelayError(nil); err != nil {
		t.Fatalf("tagRelayError(nil) = %v, want nil", err)
	}
}
