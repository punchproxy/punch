package api

import (
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
