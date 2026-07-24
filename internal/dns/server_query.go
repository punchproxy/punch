package dns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/punchproxy/punch/internal/config"
)

// Decision represents what the DNS server decided for a query.
type Decision string

const (
	DecisionIgnore Decision = "IGNORE"
	DecisionRelay  Decision = "RELAY"
	DecisionDirect Decision = "DIRECT"
	DecisionReject Decision = "REJECT"
)

func (s *Server) serveMsg(ctx context.Context, r *dns.Msg, source string) (*dns.Msg, Decision, string, string, string, error) {
	return s.serveMsgWithOptions(ctx, r, source, false, nil, false)
}

func (s *Server) serveMsgWithOptions(ctx context.Context, r *dns.Msg, source string, disableFakeIP bool, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, Decision, string, string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	s.totalQueries.Add(1)

	if len(r.Question) == 0 {
		return nil, "", "", "", "", fmt.Errorf("empty dns question")
	}

	q := r.Question[0]
	domain := strings.TrimSuffix(q.Name, ".")

	var resp *dns.Msg
	var decision Decision
	var rule string
	var resultStr string
	var upstream string

	if match, ok := s.matchDNSRule(domain, q.Qtype); ok {
		s.incrementRuleHit(match.Decision+"-domains", match.Source)
		switch match.Decision {
		case config.DecisionReject:
			resp = s.buildRejectResponse(r)
			decision = DecisionReject
			rule = "reject-domain"
			s.rejectDecisions.Add(1)
		case config.DecisionRelay:
			resp, upstream, resultStr = s.handleRelayRule(ctx, r, domain, q.Qtype, disableFakeIP, resolverOverride, respectAnswerTTL)
			decision = DecisionRelay
			rule = "relay-domain"
			s.relayDecisions.Add(1)
		case config.DecisionDirect:
			resp, upstream, resultStr = s.resolveAndCacheWithResolver(ctx, r, domain, q.Qtype, resolverOverride, respectAnswerTTL)
			decision = DecisionDirect
			rule = "direct-domain"
			s.directDecisions.Add(1)
		}
	} else if q.Qtype == dns.TypeA || q.Qtype == dns.TypeAAAA {
		resp, decision, rule, upstream, resultStr = s.resolveAndClassifyWithResolver(ctx, r, domain, q.Qtype, disableFakeIP, resolverOverride, respectAnswerTTL)
	} else {
		resp, upstream, resultStr = s.resolveAndCacheWithResolver(ctx, r, domain, q.Qtype, resolverOverride, respectAnswerTTL)
		decision = DecisionDirect
		rule = "passthrough"
	}

	if resp == nil {
		resp = new(dns.Msg)
		resp.SetRcode(r, dns.RcodeServerFailure)
	}

	resp.SetReply(r)
	resp.RecursionAvailable = true

	if resultStr == "" {
		resultStr = answerToString(resp)
		if resultStr == "" && q.Qtype == dns.TypeAAAA && decision == DecisionRelay {
			resultStr = "EMPTY(NOERROR)"
		}
	}

	latency := time.Since(start).Milliseconds()
	ql := QueryLog{
		Time:     start,
		Source:   source,
		Domain:   domain,
		QType:    qtypeName(q.Qtype),
		Decision: decision,
		Result:   resultStr,
		Upstream: upstream,
		Latency:  latency,
		Rule:     rule,
		Cached:   strings.HasPrefix(upstream, "Cache"),
	}
	s.addQueryLog(ql)
	s.fanoutQueryLog(ql)

	slog.Debug("dns resolution",
		"domain", domain,
		"qtype", ql.QType,
		"decision", string(decision),
		"result", resultStr,
		"rule", rule,
		"latency_ms", latency,
	)

	return resp, decision, rule, upstream, resultStr, nil
}

// QueryResolution is a single DNS lookup run through Punch's normal rule,
// upstream, cache, and fake-IP decision path.
type QueryResolution struct {
	Domain   string      `json:"domain"`
	QType    string      `json:"qtype"`
	Decision Decision    `json:"decision"`
	Rule     string      `json:"rule"`
	Upstream string      `json:"upstream"`
	Response string      `json:"response"`
	RCode    string      `json:"rcode"`
	Latency  int64       `json:"latency_ms"`
	Cached   bool        `json:"cached"`
	Answers  []DNSAnswer `json:"answers,omitempty"`
}

// DNSAnswer is one resource record from a resolved DNS response.
type DNSAnswer struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	Value string `json:"value"`
}

// ResolveQuery resolves domain with qtype through the server's normal DNS path
// and returns the observable decision details for diagnostics.
func (s *Server) ResolveQuery(ctx context.Context, domain string, qtype uint16) (QueryResolution, error) {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if domain == "" {
		return QueryResolution{}, fmt.Errorf("missing domain")
	}

	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(domain), qtype)

	start := time.Now()
	resp, decision, rule, upstream, response, err := s.serveMsgWithOptions(ctx, query, "punchctl", false, nil, false)
	if err != nil {
		return QueryResolution{}, err
	}

	result := QueryResolution{
		Domain:   domain,
		QType:    qtypeName(qtype),
		Decision: decision,
		Rule:     rule,
		Upstream: upstream,
		Response: response,
		Latency:  time.Since(start).Milliseconds(),
		Cached:   strings.HasPrefix(upstream, "Cache"),
	}
	if resp != nil {
		result.RCode = rcodeName(resp.Rcode)
		result.Answers = dnsAnswers(resp.Answer)
		if result.Response == "" {
			result.Response = responseText(resp)
		}
	}
	if result.Response == "" {
		result.Response = result.RCode
	}
	return result, nil
}

func qtypeName(qtype uint16) string {
	if name := dns.TypeToString[qtype]; name != "" {
		return name
	}
	return fmt.Sprintf("TYPE%d", qtype)
}

func rcodeName(rcode int) string {
	if name := dns.RcodeToString[rcode]; name != "" {
		return name
	}
	return fmt.Sprintf("RCODE%d", rcode)
}

func dnsAnswers(rrs []dns.RR) []DNSAnswer {
	if len(rrs) == 0 {
		return nil
	}
	answers := make([]DNSAnswer, 0, len(rrs))
	for _, rr := range rrs {
		header := rr.Header()
		answers = append(answers, DNSAnswer{
			Name:  header.Name,
			Type:  qtypeName(header.Rrtype),
			TTL:   header.Ttl,
			Value: rrValue(rr),
		})
	}
	return answers
}

func responseText(msg *dns.Msg) string {
	if msg == nil {
		return ""
	}
	if text := answerToString(msg); text != "" {
		return text
	}
	if len(msg.Answer) == 0 {
		return rcodeName(msg.Rcode)
	}
	parts := make([]string, 0, len(msg.Answer))
	for _, rr := range msg.Answer {
		if value := rrValue(rr); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return rcodeName(msg.Rcode)
	}
	return strings.Join(parts, ", ")
}

func rrValue(rr dns.RR) string {
	switch v := rr.(type) {
	case *dns.A:
		return v.A.String()
	case *dns.AAAA:
		return v.AAAA.String()
	case *dns.CNAME:
		return v.Target
	case *dns.NS:
		return v.Ns
	case *dns.PTR:
		return v.Ptr
	case *dns.MX:
		return fmt.Sprintf("%d %s", v.Preference, v.Mx)
	case *dns.TXT:
		return strings.Join(v.Txt, " ")
	case *dns.SRV:
		return fmt.Sprintf("%d %d %d %s", v.Priority, v.Weight, v.Port, v.Target)
	case *dns.SOA:
		return fmt.Sprintf("%s %s %d %d %d %d %d", v.Ns, v.Mbox, v.Serial, v.Refresh, v.Retry, v.Expire, v.Minttl)
	default:
		fields := strings.Fields(rr.String())
		if len(fields) > 4 {
			return strings.Join(fields[4:], " ")
		}
		return rr.String()
	}
}

func (s *Server) handleRelayRule(ctx context.Context, r *dns.Msg, domain string, qtype uint16, disableFakeIP bool, resolverOverride *ResolverGroup, respectAnswerTTL bool) (*dns.Msg, string, string) {
	switch qtype {
	case dns.TypeAAAA:
		if disableFakeIP {
			resp, upstream, result := s.resolveAndCacheWithResolver(ctx, r, domain, qtype, resolverOverride, respectAnswerTTL)
			return resp, upstream, result
		}
		if s.disableIPv6FakeIP {
			resp := new(dns.Msg)
			resp.SetReply(r)
			return resp, "", "EMPTY(NOERROR)"
		}
		fakeIP, ok := s.lookupFakeIP(domain, qtype)
		if !ok {
			resp := new(dns.Msg)
			resp.SetReply(r)
			return resp, "", "EMPTY(NOERROR)"
		}
		return s.buildFakeIPResponse(r, fakeIP), "", fakeIP.String()
	case dns.TypeA:
		if disableFakeIP {
			resp, upstream, result := s.resolveAndCacheWithResolver(ctx, r, domain, qtype, resolverOverride, respectAnswerTTL)
			return resp, upstream, result
		}
		fakeIP, ok := s.lookupFakeIP(domain, qtype)
		if !ok {
			return s.buildRejectResponse(r), "", ""
		}
		return s.buildFakeIPResponse(r, fakeIP), "", fakeIP.String()
	default:
		resp, upstream := s.resolveUpstreamWithResolver(ctx, r, resolverOverride)
		return resp, upstream, ""
	}
}

func (s *Server) processUpstreamResponse(r *dns.Msg, domain string, qtype uint16, disableFakeIP bool, resp *dns.Msg, upstream string, result string) (*dns.Msg, Decision, string, string, string) {
	if !responseHasIPAnswer(resp) {
		s.ignoreDecisions.Add(1)
		return resp, DecisionIgnore, "no-ip-detected", upstream, result
	}
	if s.responseHasDirectIP(resp) {
		s.directDecisions.Add(1)
		return resp, DecisionDirect, "ip-direct", upstream, result
	}
	s.defaultRuleHits.Add(1)
	if disableFakeIP {
		s.directDecisions.Add(1)
		return resp, DecisionDirect, "forced-direct", upstream, result
	}

	s.relayDecisions.Add(1)
	if qtype == dns.TypeAAAA && s.disableIPv6FakeIP {
		empty := new(dns.Msg)
		empty.SetReply(r)
		return empty, DecisionRelay, "ipv6-fakeip-disabled", upstream, "EMPTY(NOERROR)"
	}
	if qtype == dns.TypeA || qtype == dns.TypeAAAA {
		fakeIP, ok := s.lookupFakeIP(domain, qtype)
		if !ok {
			return s.buildRejectResponse(r), DecisionRelay, "fake-ip-family-unavailable", upstream, ""
		}
		return s.buildFakeIPResponse(r, fakeIP), DecisionRelay, "ip-fallback", upstream, fakeIP.String()
	}
	return s.buildRejectResponse(r), DecisionRelay, "relay-unsupported-qtype", upstream, ""
}

// processCachedResponse mirrors processUpstreamResponse but avoids materializing
// the cached DNS message when classification will return a synthesized response.
func (s *Server) processCachedResponse(r *dns.Msg, domain string, qtype uint16, disableFakeIP bool, hit cacheHit, upstream string) (*dns.Msg, Decision, string, string, string) {
	if hit.msg == nil {
		s.ignoreDecisions.Add(1)
		return nil, DecisionIgnore, "no-ip-detected", upstream, hit.queryResult
	}
	if !answerRRsHaveIPAnswer(hit.msg.Answer) {
		s.ignoreDecisions.Add(1)
		return hit.message(), DecisionIgnore, "no-ip-detected", upstream, hit.queryResult
	}
	if s.answerRRsHaveDirectIP(hit.msg.Answer) {
		s.directDecisions.Add(1)
		return hit.message(), DecisionDirect, "ip-direct", upstream, hit.queryResult
	}
	s.defaultRuleHits.Add(1)
	if disableFakeIP {
		s.directDecisions.Add(1)
		return hit.message(), DecisionDirect, "forced-direct", upstream, hit.queryResult
	}

	s.relayDecisions.Add(1)
	if qtype == dns.TypeAAAA && s.disableIPv6FakeIP {
		empty := new(dns.Msg)
		empty.SetReply(r)
		return empty, DecisionRelay, "ipv6-fakeip-disabled", upstream, "EMPTY(NOERROR)"
	}
	if qtype == dns.TypeA || qtype == dns.TypeAAAA {
		fakeIP, ok := s.lookupFakeIP(domain, qtype)
		if !ok {
			return s.buildRejectResponse(r), DecisionRelay, "fake-ip-family-unavailable", upstream, ""
		}
		return s.buildFakeIPResponse(r, fakeIP), DecisionRelay, "ip-fallback", upstream, fakeIP.String()
	}
	return s.buildRejectResponse(r), DecisionRelay, "relay-unsupported-qtype", upstream, ""
}

func (s *Server) responseHasDirectIP(msg *dns.Msg) bool {
	if msg == nil {
		return false
	}
	return s.answerRRsHaveDirectIP(msg.Answer)
}

func (s *Server) answerRRsHaveDirectIP(answer []dns.RR) bool {
	for _, rr := range answer {
		switch v := rr.(type) {
		case *dns.A:
			ip, ok := netip.AddrFromSlice(v.A)
			if ok {
				if source := s.directIPSource(ip); source != "" {
					s.incrementRuleHit("direct-cidrs", source)
					return true
				}
			}
		case *dns.AAAA:
			ip, ok := netip.AddrFromSlice(v.AAAA)
			if ok {
				if source := s.directIPSource(ip); source != "" {
					s.incrementRuleHit("direct-cidrs", source)
					return true
				}
			}
		}
	}
	return false
}

func responseHasIPAnswer(msg *dns.Msg) bool {
	if msg == nil {
		return false
	}
	return answerRRsHaveIPAnswer(msg.Answer)
}

func answerRRsHaveIPAnswer(answer []dns.RR) bool {
	for _, rr := range answer {
		switch rr.(type) {
		case *dns.A, *dns.AAAA:
			return true
		}
	}
	return false
}

func answerMinTTL(msg *dns.Msg) uint32 {
	if msg == nil || len(msg.Answer) == 0 {
		return 0
	}
	minTTL := uint32(^uint32(0))
	for _, rr := range msg.Answer {
		if rr.Header().Ttl < minTTL {
			minTTL = rr.Header().Ttl
		}
	}
	if minTTL == uint32(^uint32(0)) {
		return 0
	}
	return minTTL
}

func (s *Server) buildFakeIPResponse(r *dns.Msg, ip netip.Addr) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.RecursionAvailable = true

	rr := &dns.A{
		Hdr: dns.RR_Header{
			Name:   r.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: ip.AsSlice(),
	}
	if ip.Is6() {
		resp.Answer = []dns.RR{&dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   r.Question[0].Name,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
				Ttl:    300,
			},
			AAAA: ip.AsSlice(),
		}}
		return resp
	}
	resp.Answer = []dns.RR{rr}
	return resp
}

func (s *Server) buildRejectResponse(r *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.RecursionAvailable = true

	q := r.Question[0]
	if q.Qtype == dns.TypeA {
		rr := &dns.A{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    3600,
			},
			A: net.IPv4(0, 0, 0, 0),
		}
		resp.Answer = []dns.RR{rr}
	} else if q.Qtype == dns.TypeAAAA {
		rr := &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
				Ttl:    3600,
			},
			AAAA: net.IPv6zero,
		}
		resp.Answer = []dns.RR{rr}
	}
	return resp
}

func answerToString(msg *dns.Msg) string {
	if msg == nil || len(msg.Answer) == 0 {
		return ""
	}
	var parts []string
	hasNonCNAME := false
	for _, rr := range msg.Answer {
		switch rr.(type) {
		case *dns.A, *dns.AAAA:
			hasNonCNAME = true
		}
	}
	for _, rr := range msg.Answer {
		switch v := rr.(type) {
		case *dns.A:
			parts = append(parts, v.A.String())
		case *dns.AAAA:
			parts = append(parts, v.AAAA.String())
		case *dns.CNAME:
			if hasNonCNAME {
				parts = append(parts, v.Target)
			}
		}
	}
	return strings.Join(parts, ", ")
}
