package tun

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/punchproxy/punch/internal/session"
)

const tcpFallbackDrainTimeout = 30 * time.Second

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
// stream termination and whether the relay side caused it. Errors caused by
// a Punch-initiated shutdown are expected. Before shutdown, a relay-side
// closed connection, reset, or broken pipe is an actual stream abort; the same
// errors from the client just mean the local application went away.
func classifyCopyError(err error, shutdownInitiated bool) (abnormal, relaySide bool) {
	if err == nil || errors.Is(err, io.EOF) || shutdownInitiated {
		return false, false
	}
	var tagged *relaySideError
	fromRelay := errors.As(err, &tagged)
	if errors.Is(err, net.ErrClosed) {
		return fromRelay, fromRelay
	}
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return fromRelay, fromRelay
	}
	return true, fromRelay
}

type tcpShutdownState struct {
	initiated atomic.Bool
}

func (s *tcpShutdownState) begin() {
	s.initiated.Store(true)
}

type tcpCopyResult struct {
	dir               string
	err               error
	shutdownInitiated bool
}

// runTCPRelay copies a TCP stream in both directions while preserving TCP
// half-close semantics. Some multiplexed relays (including AnyTLS) cannot
// express a half-close; those connections get a bounded idle drain window
// instead, which extends while the remaining direction is making progress.
func runTCPRelay(
	ctx context.Context,
	client net.Conn,
	remote net.Conn,
	sess *session.Session,
	shutdown *tcpShutdownState,
	closeBoth func(),
	fallbackDrainTimeout time.Duration,
) tcpCopyResult {
	remoteTagged := relayTaggedConn{Conn: remote}
	activity := newTCPActivity()
	uploadSource := tcpActivityReader{
		Reader:   session.NewTrackedConn(client, sess, true),
		activity: activity,
	}
	downloadDestination := tcpActivityWriter{
		Writer:   session.NewTrackedConn(client, sess, false),
		activity: activity,
	}
	results := make(chan tcpCopyResult, 2)
	copyDirection := func(dir string, dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		results <- tcpCopyResult{
			dir:               dir,
			err:               err,
			shutdownInitiated: shutdown.initiated.Load(),
		}
	}
	go copyDirection("upload", remoteTagged, uploadSource)
	go copyDirection("download", downloadDestination, remoteTagged)

	var first tcpCopyResult
	select {
	case first = <-results:
	case <-ctx.Done():
		closeBoth()
		<-results
		<-results
		return tcpCopyResult{dir: "session", err: context.Cause(ctx), shutdownInitiated: true}
	}

	// If Punch already initiated shutdown (for example via KillSession), both
	// copies are already being stopped and their close errors are expected.
	if first.shutdownInitiated {
		<-results
		return first
	}

	abnormal, _ := classifyCopyError(first.err, false)
	if abnormal || first.err != nil {
		closeBoth()
		<-results
		return first
	}

	// Give a no-half-close transport a full grace period from the point the
	// first direction finishes, regardless of how old the session is.
	activity.touch()
	halfClosed := propagateTCPHalfClose(first.dir, client, remote)

	var drainTimer *time.Timer
	var drainC <-chan time.Time
	if !halfClosed {
		drainTimer = time.NewTimer(fallbackDrainTimeout)
		drainC = drainTimer.C
		defer drainTimer.Stop()
	}

	for {
		select {
		case second := <-results:
			secondAbnormal, _ := classifyCopyError(second.err, second.shutdownInitiated)
			if second.err == nil {
				propagateTCPHalfClose(second.dir, client, remote)
			}
			if secondAbnormal || second.err != nil {
				closeBoth()
			}
			if secondAbnormal {
				return second
			}
			return first
		case <-ctx.Done():
			closeBoth()
			<-results
			return tcpCopyResult{dir: "session", err: context.Cause(ctx), shutdownInitiated: true}
		case <-drainC:
			if remaining := fallbackDrainTimeout - activity.idleFor(); remaining > 0 {
				drainTimer.Reset(remaining)
				continue
			}
			closeBoth()
			<-results
			return tcpCopyResult{dir: first.dir, shutdownInitiated: true}
		}
	}
}

type tcpActivity struct {
	started time.Time
	last    atomic.Int64
}

func newTCPActivity() *tcpActivity {
	return &tcpActivity{started: time.Now()}
}

func (a *tcpActivity) touch() {
	a.last.Store(int64(time.Since(a.started)))
}

func (a *tcpActivity) idleFor() time.Duration {
	return time.Since(a.started) - time.Duration(a.last.Load())
}

type tcpActivityReader struct {
	io.Reader
	activity *tcpActivity
}

func (r tcpActivityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity.touch()
	}
	return n, err
}

type tcpActivityWriter struct {
	io.Writer
	activity *tcpActivity
}

func (w tcpActivityWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.activity.touch()
	}
	return n, err
}

func propagateTCPHalfClose(dir string, client, remote net.Conn) bool {
	if dir == "upload" {
		return tryCloseWrite(remote)
	}
	return tryCloseWrite(client)
}

type closeWriter interface {
	CloseWrite() error
}

func tryCloseWrite(conn net.Conn) bool {
	connWithCloseWrite, ok := conn.(closeWriter)
	if !ok {
		return false
	}
	return connWithCloseWrite.CloseWrite() == nil
}
