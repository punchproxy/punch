package dnsrule

import "testing"

func TestNormalizeDefaultsBareRuleToDomain(t *testing.T) {
	got := Normalize(" google.com ")
	if got != "domain:google.com" {
		t.Fatalf("Normalize() = %q, want %q", got, "domain:google.com")
	}
}

func TestSplitPreservesRegexpPatternCase(t *testing.T) {
	kind, value := Split(`Regexp:.+\.Google\.COM$`)
	if kind != KindRegexp {
		t.Fatalf("Split() kind = %q, want %q", kind, KindRegexp)
	}
	if value != `.+\.Google\.COM$` {
		t.Fatalf("Split() value = %q", value)
	}
}

func TestSplitBareRule(t *testing.T) {
	kind, value := Split("example.com")
	if kind != KindDomain {
		t.Fatalf("Split() kind = %q, want %q", kind, KindDomain)
	}
	if value != "example.com" {
		t.Fatalf("Split() value = %q, want %q", value, "example.com")
	}
}

func TestNormalizePreservesQTypeRule(t *testing.T) {
	got := Normalize(" QType:PTR ")
	if got != "qtype:ptr" {
		t.Fatalf("Normalize() = %q, want %q", got, "qtype:ptr")
	}
}

func TestParseQTypeAcceptsNumberAndName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want uint16
	}{
		{in: "65", want: 65},
		{in: "https", want: 65},
		{in: "TYPE65", want: 65},
		{in: "ptr", want: 12},
	} {
		got, err := ParseQType(tc.in)
		if err != nil {
			t.Fatalf("ParseQType(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseQType(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestMatcherUsesRuleOrderAcrossRuleKinds(t *testing.T) {
	matcher := NewMatcher()
	if err := matcher.AddRule("full:www.example.com", "direct", 1); err != nil {
		t.Fatalf("AddRule(full) error = %v", err)
	}
	if err := matcher.AddRule("domain:example.com", "relay", 0); err != nil {
		t.Fatalf("AddRule(domain) error = %v", err)
	}

	match, ok := matcher.Match("www.example.com")
	if !ok {
		t.Fatal("Match() did not match")
	}
	if match.Decision != "relay" {
		t.Fatalf("Match() decision = %q, want relay", match.Decision)
	}
}

func TestMatcherUsesFirstRuleWhenPatternIsDuplicated(t *testing.T) {
	matcher := NewMatcher()
	if err := matcher.AddRule("domain:example.com", "relay", 0); err != nil {
		t.Fatalf("AddRule(relay) error = %v", err)
	}
	if err := matcher.AddRule("domain:example.com", "reject", 1); err != nil {
		t.Fatalf("AddRule(reject) error = %v", err)
	}

	match, ok := matcher.Match("api.example.com")
	if !ok {
		t.Fatal("Match() did not match")
	}
	if match.Decision != "relay" {
		t.Fatalf("Match() decision = %q, want relay", match.Decision)
	}
}

func TestMatcherUsesRuleOrderAcrossDomainAndQType(t *testing.T) {
	matcher := NewMatcher()
	if err := matcher.AddRule("domain:example.com", "relay", 0); err != nil {
		t.Fatalf("AddRule(domain) error = %v", err)
	}
	if err := matcher.AddRule("qtype:65", "reject", 1); err != nil {
		t.Fatalf("AddRule(qtype) error = %v", err)
	}

	match, ok := matcher.MatchQuery("www.example.com", 65)
	if !ok {
		t.Fatal("MatchQuery() did not match")
	}
	if match.Decision != "relay" {
		t.Fatalf("MatchQuery() decision = %q, want relay", match.Decision)
	}
}
