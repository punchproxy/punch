package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHandleShutdown(t *testing.T) {
	called := make(chan struct{})
	s := &Server{shutdown: func() { close(called) }}

	rec := runRelayHandler(t, s.handleShutdown, http.MethodPost, "/api/shutdown", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestHandleShutdownUnavailable(t *testing.T) {
	s := &Server{}

	rec := runRelayHandler(t, s.handleShutdown, http.MethodPost, "/api/shutdown", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestStopCancelsActiveRequests(t *testing.T) {
	requestStarted := make(chan struct{})
	requestDone := make(chan struct{})
	s := &Server{}
	s.configureHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestDone)
		w.WriteHeader(http.StatusNoContent)
	}))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- s.httpServer.Serve(listener)
	}()

	responseDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+listener.Addr().String(), nil)
		if err != nil {
			responseDone <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		responseDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach handler")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("active request context was not canceled")
	}
	select {
	case err := <-serveDone:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatalf("request error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}
}
