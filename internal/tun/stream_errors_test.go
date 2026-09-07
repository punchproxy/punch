package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/session"
)

func TestClassifyCopyError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		shutdown bool
		abnormal bool
		relay    bool
		relayTyp string
	}{
		{"clean close", nil, false, false, false, ""},
		{"eof", io.EOF, false, false, false, ""},
		{"client closed connection", net.ErrClosed, false, false, false, ""},
		{"session torn down", net.ErrClosed, true, false, false, ""},
		{"relay closed before teardown", tagRelayError(net.ErrClosed), false, true, true, ""},
		{"tagged closed conn from teardown", tagRelayError(net.ErrClosed), true, false, false, ""},
		{"client reset (app went away)", syscall.ECONNRESET, false, false, false, ""},
		{"client broken pipe", syscall.EPIPE, false, false, false, ""},
		{"relay reset mid-stream", tagRelayError(syscall.ECONNRESET), false, true, true, ""},
		{"relay broken pipe", tagRelayError(syscall.EPIPE), false, true, true, ""},
		{"relay reset wrapped in op error", tagRelayError(fmt.Errorf("read tcp: %w", syscall.ECONNRESET)), false, true, true, ""},
		{"relay generic error", tagRelayError(errors.New("tls: bad record")), false, true, true, ""},
		{"client generic error", errors.New("short write"), false, true, false, ""},
		{"generic teardown error", errors.New("short write"), true, false, false, ""},
		{"ss relay reset is a normal end", tagRelayError(syscall.ECONNRESET), false, false, false, "ss"},
		{"ss relay broken pipe is a normal end", tagRelayError(syscall.EPIPE), false, false, false, "ss"},
		{"shadowsocks adapter reset is a normal end", tagRelayError(syscall.ECONNRESET), false, false, false, "Shadowsocks"},
		{"ssr relay reset is a normal end", tagRelayError(syscall.ECONNRESET), false, false, false, "ssr"},
		{"ss reset wrapped in op error is a normal end", tagRelayError(fmt.Errorf("read tcp: %w", syscall.ECONNRESET)), false, false, false, "ss"},
		{"non-ss relay reset is still an abort", tagRelayError(syscall.ECONNRESET), false, true, true, "vmess"},
		{"ss relay generic error stays abnormal", tagRelayError(errors.New("tls: bad record")), false, true, true, "ss"},
	}
	for _, tc := range cases {
		abnormal, relay := classifyCopyError(tc.err, tc.shutdown, tc.relayTyp)
		if abnormal != tc.abnormal || relay != tc.relay {
			t.Errorf("%s: classifyCopyError() = (%v, %v), want (%v, %v)", tc.name, abnormal, relay, tc.abnormal, tc.relay)
		}
	}
}

func TestRunTCPRelayPreservesClientHalfCloseForDelayedResponse(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	serverResult := make(chan []byte, 1)
	go func() {
		request, _ := io.ReadAll(server)
		time.Sleep(20 * time.Millisecond)
		_, _ = server.Write([]byte("response"))
		_ = server.CloseWrite()
		serverResult <- request
	}()

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close client write: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read delayed response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("response = %q, want response", response)
	}
	if request := <-serverResult; string(request) != "request" {
		t.Fatalf("request = %q, want request", request)
	}
	assertCleanTCPResult(t, awaitTCPResult(t, result), "")
}

func TestRunTCPRelayPreservesUploadAfterServerFIN(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	serverResult := make(chan []byte, 1)
	go func() {
		_, _ = server.Write([]byte("response"))
		_ = server.CloseWrite()
		upload, _ := io.ReadAll(server)
		serverResult <- upload
	}()

	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("response = %q, want response", response)
	}
	if _, err := client.Write([]byte("late upload")); err != nil {
		t.Fatalf("write after server FIN: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close client write: %v", err)
	}
	if upload := <-serverResult; string(upload) != "late upload" {
		t.Fatalf("upload = %q, want late upload", upload)
	}
	assertCleanTCPResult(t, awaitTCPResult(t, result), "")
}

func TestRunTCPRelayDrainsTransportWithoutCloseWrite(t *testing.T) {
	client, local := newHalfClosePipe()
	remotePipe, server := newHalfClosePipe()
	remote := struct{ net.Conn }{Conn: remotePipe}
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	go func() {
		request := make([]byte, len("request"))
		_, _ = io.ReadFull(server, request)
		time.Sleep(20 * time.Millisecond)
		_, _ = server.Write([]byte("response"))
		_ = server.CloseWrite()
	}()

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close client write: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("response = %q, want response", response)
	}
	assertCleanTCPResult(t, awaitTCPResult(t, result), "")
}

func TestRunTCPRelayBoundsDrainWithoutCloseWrite(t *testing.T) {
	client, local := newHalfClosePipe()
	remotePipe, server := newHalfClosePipe()
	remote := struct{ net.Conn }{Conn: remotePipe}
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, 20*time.Millisecond, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close client write: %v", err)
	}
	got := awaitTCPResult(t, result)
	if !got.shutdownInitiated {
		t.Fatalf("fallback drain result = %+v, want Punch-initiated shutdown", got)
	}
	assertCleanTCPResult(t, got, "")
}

func TestRunTCPRelayExtendsFallbackDrainWhileActive(t *testing.T) {
	client, local := newHalfClosePipe()
	remotePipe, server := newHalfClosePipe()
	remote := struct{ net.Conn }{Conn: remotePipe}
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, 100*time.Millisecond, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	go func() {
		time.Sleep(25 * time.Millisecond)
		for _, chunk := range []byte("active") {
			_, _ = server.Write([]byte{chunk})
			time.Sleep(25 * time.Millisecond)
		}
		_ = server.CloseWrite()
	}()

	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close client write: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read active response: %v", err)
	}
	if string(response) != "active" {
		t.Fatalf("response = %q, want active", response)
	}
	assertCleanTCPResult(t, awaitTCPResult(t, result), "")
}

func TestRunTCPRelayReportsRelayClosedBeforeShutdown(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "")
	defer closeBoth()
	defer client.Close()

	server.closeWithError(net.ErrClosed)
	got := awaitTCPResult(t, result)
	abnormal, relay := classifyCopyError(got.err, got.shutdownInitiated, "")
	if !abnormal || !relay {
		t.Fatalf("relay close classified as abnormal=%v relay=%v; result=%+v", abnormal, relay, got)
	}
}

func TestRunTCPRelayTreatsShadowsocksResetAsClean(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "ss")
	defer closeBoth()
	defer client.Close()

	server.closeWithError(syscall.ECONNRESET)
	got := awaitTCPResult(t, result)
	assertCleanTCPResult(t, got, "ss")
}

func TestRunTCPRelayReportsNonSSResetAsAbort(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	result, closeBoth := startTCPRelay(t, context.Background(), local, remote, time.Second, "vmess")
	defer closeBoth()
	defer client.Close()

	server.closeWithError(syscall.ECONNRESET)
	got := awaitTCPResult(t, result)
	abnormal, relay := classifyCopyError(got.err, got.shutdownInitiated, "vmess")
	if !abnormal || !relay {
		t.Fatalf("non-SS relay reset classified as abnormal=%v relay=%v; result=%+v", abnormal, relay, got)
	}
}

func TestRunTCPRelayCancellationIsCleanShutdown(t *testing.T) {
	client, local := newHalfClosePipe()
	remote, server := newHalfClosePipe()
	ctx, cancel := context.WithCancel(context.Background())
	result, closeBoth := startTCPRelay(t, ctx, local, remote, time.Second, "")
	defer closeBoth()
	defer client.Close()
	defer server.Close()

	cancel()
	got := awaitTCPResult(t, result)
	if !errors.Is(got.err, context.Canceled) || !got.shutdownInitiated {
		t.Fatalf("cancellation result = %+v, want canceled Punch shutdown", got)
	}
	assertCleanTCPResult(t, got, "")
}

func TestTagRelayErrorPreservesEOFForIOCopy(t *testing.T) {
	if err := tagRelayError(io.EOF); err != io.EOF {
		t.Fatalf("tagRelayError(io.EOF) = %v, want untouched io.EOF", err)
	}
	if err := tagRelayError(nil); err != nil {
		t.Fatalf("tagRelayError(nil) = %v, want nil", err)
	}
}

func startTCPRelay(t *testing.T, ctx context.Context, client, remote net.Conn, drainTimeout time.Duration, relayType string) (<-chan tcpCopyResult, func()) {
	t.Helper()
	shutdown := &tcpShutdownState{}
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			shutdown.begin()
			_ = client.Close()
			_ = remote.Close()
		})
	}
	result := make(chan tcpCopyResult, 1)
	go func() {
		result <- runTCPRelay(ctx, client, remote, &session.Session{}, shutdown, closeBoth, drainTimeout, relayType)
	}()
	return result, closeBoth
}

func assertCleanTCPResult(t *testing.T, result tcpCopyResult, relayType string) {
	t.Helper()
	if abnormal, relay := classifyCopyError(result.err, result.shutdownInitiated, relayType); abnormal || relay {
		t.Fatalf("TCP result = %+v, classified as abnormal=%v relay=%v", result, abnormal, relay)
	}
}

func awaitTCPResult(t *testing.T, result <-chan tcpCopyResult) tcpCopyResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TCP relay result")
		return tcpCopyResult{}
	}
}

type halfClosePipeConn struct {
	reader    *io.PipeReader
	writer    *io.PipeWriter
	closeOnce sync.Once
}

func newHalfClosePipe() (*halfClosePipeConn, *halfClosePipeConn) {
	leftReader, rightWriter := io.Pipe()
	rightReader, leftWriter := io.Pipe()
	return &halfClosePipeConn{reader: leftReader, writer: leftWriter},
		&halfClosePipeConn{reader: rightReader, writer: rightWriter}
}

func (c *halfClosePipeConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *halfClosePipeConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *halfClosePipeConn) LocalAddr() net.Addr         { return pipeAddr("local") }
func (c *halfClosePipeConn) RemoteAddr() net.Addr        { return pipeAddr("remote") }
func (c *halfClosePipeConn) SetDeadline(time.Time) error { return nil }
func (c *halfClosePipeConn) SetReadDeadline(time.Time) error {
	return nil
}
func (c *halfClosePipeConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *halfClosePipeConn) CloseWrite() error {
	return c.writer.Close()
}

func (c *halfClosePipeConn) Close() error {
	c.closeWithError(net.ErrClosed)
	return nil
}

func (c *halfClosePipeConn) closeWithError(err error) {
	c.closeOnce.Do(func() {
		_ = c.reader.CloseWithError(err)
		_ = c.writer.CloseWithError(err)
	})
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }
