package dns

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"

	mdns "github.com/miekg/dns"
)

type upstreamTestTransport func(*http.Request) (*http.Response, error)

func (f upstreamTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpstreamResolverCountsErrors(t *testing.T) {
	tests := []struct {
		name      string
		rcode     int
		status    int
		err       error
		malformed bool
		cancel    bool
		wantError bool
		wantCount int64
	}{
		{name: "success"},
		{name: "nxdomain", rcode: mdns.RcodeNameError},
		{name: "servfail", rcode: mdns.RcodeServerFailure, wantCount: 1},
		{name: "refused", rcode: mdns.RcodeRefused, wantCount: 1},
		{name: "format error", rcode: mdns.RcodeFormatError, wantCount: 1},
		{name: "http failure", status: http.StatusServiceUnavailable, wantError: true, wantCount: 1},
		{name: "invalid dns reply", malformed: true, wantError: true, wantCount: 1},
		{name: "request failure", err: errors.New("connection refused"), wantError: true, wantCount: 1},
		{name: "context timeout", err: context.DeadlineExceeded, wantError: true, wantCount: 1},
		{name: "network timeout", err: &net.OpError{Op: "read", Net: "udp", Err: os.ErrDeadlineExceeded}, wantError: true, wantCount: 1},
		{name: "canceled request", err: context.Canceled, wantError: true},
		{name: "canceled context", err: errors.New("read interrupted"), cancel: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			query := new(mdns.Msg)
			query.SetQuestion("errors.example.", mdns.TypeA)
			reply := new(mdns.Msg)
			reply.SetRcode(query, tt.rcode)
			body, err := reply.Pack()
			if err != nil {
				t.Fatalf("pack reply: %v", err)
			}
			if tt.malformed {
				body = []byte("invalid")
			}
			upstream := NewUpstreamResolver("https://dns.example/dns-query", "")
			upstream.client = &http.Client{Transport: upstreamTestTransport(func(*http.Request) (*http.Response, error) {
				if tt.cancel {
					cancel()
				}
				if tt.err != nil {
					return nil, tt.err
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})}
			if got := upstream.Stats().Errors; got != 0 {
				t.Fatalf("initial errors = %d, want 0", got)
			}
			for i := int64(1); i <= 2; i++ {
				_, _, err := upstream.Resolve(ctx, query.Copy())
				if (err != nil) != tt.wantError {
					t.Fatalf("Resolve() error = %v, want error %v", err, tt.wantError)
				}
				if got := upstream.Stats().Errors; got != i*tt.wantCount {
					t.Fatalf("errors after %d attempts = %d, want %d", i, got, i*tt.wantCount)
				}
			}
		})
	}
}

func TestResolverGroupCountsEachUpstreamsErrors(t *testing.T) {
	upstreams := make([]*UpstreamResolver, 3)
	for i := range upstreams {
		upstreams[i] = NewUpstreamResolver(fmt.Sprintf("https://dns%d.example/dns-query", i), "")
		upstreams[i].client = &http.Client{Transport: upstreamTestTransport(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})}
	}
	group := NewResolverGroup(upstreams)
	query := new(mdns.Msg)
	query.SetQuestion("errors.example.", mdns.TypeA)
	if _, err := group.Resolve(context.Background(), query); err == nil {
		t.Fatal("Resolve() succeeded with all upstreams timing out")
	}
	for _, stats := range group.UpstreamStats() {
		if stats.Errors != 1 || stats.Queries != 0 {
			t.Fatalf("upstream %s: errors = %d, queries = %d, want 1 error and 0 accepted replies", stats.URL, stats.Errors, stats.Queries)
		}
	}
}
