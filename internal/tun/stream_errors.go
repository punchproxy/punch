package tun

import (
	"errors"
	"io"
	"net"
	"syscall"
)

// relaySideError marks an error as originating from the relay-side connection
// of a proxied stream, so copy failures can be attributed to the relay rather
// than the local client.
type relaySideError struct{ err error }

func (e *relaySideError) Error() string { return e.err.Error() }
func (e *relaySideError) Unwrap() error { return e.err }

// relayTaggedConn wraps the relay-side connection of a TCP session and tags
// every error it produces.
type relayTaggedConn struct{ net.Conn }

func (c relayTaggedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	return n, tagRelayError(err)
}

func (c relayTaggedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	return n, tagRelayError(err)
}

// tagRelayError wraps err as relay-side. io.EOF is passed through untouched:
// io.Copy compares against it directly to detect a normal end of stream.
func tagRelayError(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	return &relaySideError{err: err}
}

// classifyCopyError reports whether a session copy error is an abnormal
// stream termination and whether the relay side caused it. Resets and broken
// pipes count as abnormal only when they come from the relay: from the client
// side they just mean the local application went away.
func classifyCopyError(err error) (abnormal, relaySide bool) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return false, false
	}
	var tagged *relaySideError
	fromRelay := errors.As(err, &tagged)
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return fromRelay, fromRelay
	}
	return true, fromRelay
}
