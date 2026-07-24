package dns

import (
	"testing"

	"github.com/punchproxy/punch/internal/config"
)

func TestStatsTracksLastDomainPerDecision(t *testing.T) {
	server := &Server{cache: NewCache(10, 0, 60)}
	server.relayDecisions.Add(2)
	server.directDecisions.Add(1)
	server.rejectDecisions.Add(2)

	for _, entry := range []QueryLog{
		{Decision: DecisionRelay, Domain: "relay-old.example", QType: "A"},
		{Decision: DecisionDirect, Domain: "direct.example", QType: "AAAA"},
		{Decision: DecisionReject, Domain: "reject-old.example", QType: "PTR"},
		{Decision: DecisionRelay, Domain: "relay-new.example", QType: "HTTPS"},
		{Decision: DecisionReject, Domain: "reject-new.example", QType: "SVCB"},
		{Decision: DecisionIgnore, Domain: "ignored.example", QType: "TXT"},
	} {
		server.addQueryLog(entry)
	}

	stats := server.Stats()
	if stats.Relay.Requests != 2 || stats.Relay.LastDomain != "relay-new.example" || stats.Relay.LastQType != "HTTPS" {
		t.Fatalf("relay stats = %+v, want 2 requests and relay-new.example HTTPS", stats.Relay)
	}
	if stats.Direct.Requests != 1 || stats.Direct.LastDomain != "direct.example" || stats.Direct.LastQType != "AAAA" {
		t.Fatalf("direct stats = %+v, want 1 request and direct.example AAAA", stats.Direct)
	}
	if stats.Reject.Requests != 2 || stats.Reject.LastDomain != "reject-new.example" || stats.Reject.LastQType != "SVCB" {
		t.Fatalf("reject stats = %+v, want 2 requests and reject-new.example SVCB", stats.Reject)
	}
}

func TestStatsTracksConfigDecisionValues(t *testing.T) {
	server := &Server{cache: NewCache(10, 0, 60)}

	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionRelay), Domain: "relay.example", QType: "A"})
	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionDirect), Domain: "direct.example", QType: "AAAA"})
	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionReject), Domain: "reject.example", QType: "HTTPS"})

	stats := server.Stats()
	if stats.Relay.LastDomain != "relay.example" || stats.Relay.LastQType != "A" {
		t.Fatalf("relay last query = %q %q, want relay.example A", stats.Relay.LastDomain, stats.Relay.LastQType)
	}
	if stats.Direct.LastDomain != "direct.example" || stats.Direct.LastQType != "AAAA" {
		t.Fatalf("direct last query = %q %q, want direct.example AAAA", stats.Direct.LastDomain, stats.Direct.LastQType)
	}
	if stats.Reject.LastDomain != "reject.example" || stats.Reject.LastQType != "HTTPS" {
		t.Fatalf("reject last query = %q %q, want reject.example HTTPS", stats.Reject.LastDomain, stats.Reject.LastQType)
	}
}
