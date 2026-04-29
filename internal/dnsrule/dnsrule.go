package dnsrule

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

const (
	KindDomain  = "domain"
	KindFull    = "full"
	KindKeyword = "keyword"
	KindRegexp  = "regexp"
	KindQType   = "qtype"
)

func Normalize(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" || strings.HasPrefix(rule, "#") {
		return rule
	}
	lower := strings.ToLower(rule)
	switch {
	case strings.HasPrefix(lower, "domain:"),
		strings.HasPrefix(lower, "full:"),
		strings.HasPrefix(lower, "keyword:"),
		strings.HasPrefix(lower, "regexp:"):
		return rule
	case strings.HasPrefix(lower, "qtype:"):
		return "qtype:" + strings.ToLower(strings.TrimSpace(rule[len("qtype:"):]))
	default:
		return "domain:" + rule
	}
}

func Split(rule string) (kind string, value string) {
	rule = Normalize(rule)
	lower := strings.ToLower(rule)
	for _, prefix := range []struct {
		text string
		kind string
	}{
		{text: "domain:", kind: KindDomain},
		{text: "full:", kind: KindFull},
		{text: "keyword:", kind: KindKeyword},
		{text: "regexp:", kind: KindRegexp},
		{text: "qtype:", kind: KindQType},
	} {
		if strings.HasPrefix(lower, prefix.text) {
			return prefix.kind, strings.TrimSpace(rule[len(prefix.text):])
		}
	}
	return KindDomain, rule
}

type SourceOpener interface {
	Open(source string) (io.ReadCloser, error)
}

type Match struct {
	Decision string
	Source   string
}

type Matcher struct {
	suffixRules map[string]orderedRule
	fullRules   map[string]orderedRule
	qtypeRules  map[uint16]orderedRule
	keywords    []keywordRule
	regexps     []regexpRule
	nextSeq     int
}

type orderedRule struct {
	decision string
	source   string
	order    int
	seq      int
}

type keywordRule struct {
	value string
	rule  orderedRule
}

type regexpRule struct {
	re   *regexp.Regexp
	rule orderedRule
}

func NewMatcher() *Matcher {
	return &Matcher{
		suffixRules: make(map[string]orderedRule),
		fullRules:   make(map[string]orderedRule),
		qtypeRules:  make(map[uint16]orderedRule),
	}
}

func (m *Matcher) AddRule(rule string, decision string, order int) error {
	return m.AddRuleWithSource(rule, rule, decision, order)
}

func (m *Matcher) AddRuleWithSource(rule string, source string, decision string, order int) error {
	rule = strings.TrimSpace(rule)
	if rule == "" || strings.HasPrefix(rule, "#") {
		return nil
	}

	kind, value := Split(rule)
	lowerValue := strings.ToLower(value)
	entry := orderedRule{
		decision: decision,
		source:   source,
		order:    order,
		seq:      m.nextSeq,
	}
	m.nextSeq++

	switch kind {
	case KindFull:
		m.addBest(m.fullRules, normalizeDomain(lowerValue), entry)
	case KindDomain:
		m.addBest(m.suffixRules, normalizeDomain(lowerValue), entry)
	case KindKeyword:
		m.keywords = append(m.keywords, keywordRule{value: lowerValue, rule: entry})
	case KindRegexp:
		re, err := regexp.Compile(value)
		if err != nil {
			return fmt.Errorf("invalid regexp %q: %w", value, err)
		}
		m.regexps = append(m.regexps, regexpRule{re: re, rule: entry})
	case KindQType:
		qtype, err := ParseQType(value)
		if err != nil {
			return err
		}
		m.addBestQType(qtype, entry)
	}
	return nil
}

func (m *Matcher) addBest(rules map[string]orderedRule, key string, rule orderedRule) {
	current, ok := rules[key]
	if !ok || before(rule, current) {
		rules[key] = rule
	}
}

func (m *Matcher) addBestQType(qtype uint16, rule orderedRule) {
	current, ok := m.qtypeRules[qtype]
	if !ok || before(rule, current) {
		m.qtypeRules[qtype] = rule
	}
}

func (m *Matcher) Match(domain string) (Match, bool) {
	return m.matchDomain(domain)
}

func (m *Matcher) MatchQuery(domain string, qtype uint16) (Match, bool) {
	best, matched := m.matchDomainRule(domain)
	if rule, ok := m.qtypeRules[qtype]; ok && (!matched || before(rule, best)) {
		best = rule
		matched = true
	}
	if !matched {
		return Match{}, false
	}
	return matchFromRule(best), true
}

func (m *Matcher) matchDomain(domain string) (Match, bool) {
	best, matched := m.matchDomainRule(domain)
	if !matched {
		return Match{}, false
	}
	return matchFromRule(best), true
}

func (m *Matcher) matchDomainRule(domain string) (orderedRule, bool) {
	domain = normalizeDomain(strings.ToLower(domain))
	var best orderedRule
	matched := false

	if rule, ok := m.fullRules[domain]; ok {
		best = rule
		matched = true
		if best.order == 0 {
			return best, true
		}
	}

	d := domain
	for {
		if rule, ok := m.suffixRules[d]; ok && (!matched || before(rule, best)) {
			best = rule
			matched = true
			if best.order == 0 {
				return best, true
			}
		}
		idx := strings.IndexByte(d, '.')
		if idx < 0 {
			break
		}
		d = d[idx+1:]
	}

	for _, kw := range m.keywords {
		if strings.Contains(domain, kw.value) && (!matched || before(kw.rule, best)) {
			best = kw.rule
			matched = true
			if best.order == 0 {
				return best, true
			}
		}
	}

	for _, re := range m.regexps {
		if re.re.MatchString(domain) && (!matched || before(re.rule, best)) {
			best = re.rule
			matched = true
			if best.order == 0 {
				return best, true
			}
		}
	}

	if !matched {
		return orderedRule{}, false
	}
	return best, true
}

func (m *Matcher) MatchSource(domain string) string {
	match, ok := m.Match(domain)
	if !ok {
		return ""
	}
	return match.Source
}

func (m *Matcher) Count() int {
	return len(m.suffixRules) + len(m.fullRules) + len(m.keywords) + len(m.regexps) + len(m.qtypeRules)
}

func before(a, b orderedRule) bool {
	if a.order != b.order {
		return a.order < b.order
	}
	return a.seq < b.seq
}

func matchFromRule(rule orderedRule) Match {
	return Match{Decision: rule.decision, Source: rule.source}
}

func normalizeDomain(d string) string {
	return strings.TrimSuffix(strings.TrimSpace(d), ".")
}

func ParseQType(value string) (uint16, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("missing qtype")
	}
	if n, err := strconv.ParseUint(value, 10, 16); err == nil {
		return uint16(n), nil
	}
	if strings.HasPrefix(strings.ToUpper(value), "TYPE") {
		if n, err := strconv.ParseUint(value[len("TYPE"):], 10, 16); err == nil {
			return uint16(n), nil
		}
	}
	if qtype, ok := dns.StringToType[strings.ToUpper(value)]; ok {
		return qtype, nil
	}
	return 0, fmt.Errorf("unknown qtype %q", value)
}

func FormatQType(qtype uint16) string {
	if name := dns.TypeToString[qtype]; name != "" {
		return "qtype:" + strings.ToLower(name)
	}
	return fmt.Sprintf("qtype:%d", qtype)
}

func Load(source string, matcher *Matcher, opener SourceOpener, decision string, order int) (int, error) {
	var reader io.ReadCloser
	var err error

	if opener != nil {
		reader, err = opener.Open(source)
		if err != nil {
			return 0, err
		}
	} else {
		reader, err = os.Open(source)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", source, err)
		}
	}
	defer reader.Close()

	count := 0
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := matcher.AddRuleWithSource(line, source, decision, order); err != nil {
			return count, err
		}
		count++
	}
	return count, scanner.Err()
}
