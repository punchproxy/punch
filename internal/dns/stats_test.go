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
		{Decision: DecisionRelay, Domain: "relay-old.example"},
		{Decision: DecisionDirect, Domain: "direct.example"},
		{Decision: DecisionReject, Domain: "reject-old.example"},
		{Decision: DecisionRelay, Domain: "relay-new.example"},
		{Decision: DecisionReject, Domain: "reject-new.example"},
		{Decision: DecisionIgnore, Domain: "ignored.example"},
	} {
		server.addQueryLog(entry)
	}

	stats := server.Stats()
	if stats.Relay.Requests != 2 || stats.Relay.LastDomain != "relay-new.example" {
		t.Fatalf("relay stats = %+v, want 2 requests and relay-new.example", stats.Relay)
	}
	if stats.Direct.Requests != 1 || stats.Direct.LastDomain != "direct.example" {
		t.Fatalf("direct stats = %+v, want 1 request and direct.example", stats.Direct)
	}
	if stats.Reject.Requests != 2 || stats.Reject.LastDomain != "reject-new.example" {
		t.Fatalf("reject stats = %+v, want 2 requests and reject-new.example", stats.Reject)
	}
}

func TestStatsTracksConfigDecisionValues(t *testing.T) {
	server := &Server{cache: NewCache(10, 0, 60)}

	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionRelay), Domain: "relay.example"})
	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionDirect), Domain: "direct.example"})
	server.addQueryLog(QueryLog{Decision: Decision(config.DecisionReject), Domain: "reject.example"})

	stats := server.Stats()
	if stats.Relay.LastDomain != "relay.example" {
		t.Fatalf("relay last domain = %q, want relay.example", stats.Relay.LastDomain)
	}
	if stats.Direct.LastDomain != "direct.example" {
		t.Fatalf("direct last domain = %q, want direct.example", stats.Direct.LastDomain)
	}
	if stats.Reject.LastDomain != "reject.example" {
		t.Fatalf("reject last domain = %q, want reject.example", stats.Reject.LastDomain)
	}
}
