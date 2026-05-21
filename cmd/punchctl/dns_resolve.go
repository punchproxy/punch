package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

type dnsResolveResult struct {
	Domain   string             `json:"domain"`
	QType    string             `json:"qtype"`
	Decision string             `json:"decision"`
	Rule     string             `json:"rule"`
	Upstream string             `json:"upstream"`
	Response string             `json:"response"`
	RCode    string             `json:"rcode"`
	Latency  int64              `json:"latency_ms"`
	Cached   bool               `json:"cached"`
	Answers  []dnsResolveAnswer `json:"answers,omitempty"`
}

type dnsResolveAnswer struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	Value string `json:"value"`
}

type dnsResolveRow struct {
	Domain   string    `json:"domain" yaml:"domain"`
	QType    string    `json:"qtype" yaml:"qtype"`
	Decision string    `json:"decision" yaml:"decision"`
	Upstream string    `json:"upstream" yaml:"upstream"`
	Response string    `json:"response" yaml:"response"`
	Rule     string    `json:"rule,omitempty" yaml:"rule,omitempty"`
	RCode    string    `json:"rcode,omitempty" yaml:"rcode,omitempty"`
	Latency  latencyMS `json:"latency_ms,omitempty" yaml:"latency_ms,omitempty"`
	Cached   bool      `json:"cached" yaml:"cached"`
	Answers  string    `json:"answers,omitempty" yaml:"answers,omitempty"`
}

func newResolveCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	qtype := "A"
	cmd := &cobra.Command{
		Use:   "resolve DOMAIN",
		Short: "Resolve a domain through Punch DNS",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			result, err := fetchDNSResolve(c.Context(), *cfg, args[0], qtype)
			if err != nil {
				return err
			}
			return writeDNSResolve(c.OutOrStdout(), result, flags)
		},
	}
	cmd.Flags().StringVarP(&qtype, "type", "t", qtype, "DNS query type, e.g. A, AAAA, HTTPS, or TYPE65")
	addListFlags(cmd, &flags)
	return cmd
}

func fetchDNSResolve(ctx context.Context, cfg commandConfig, domain, qtype string) (dnsResolveResult, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	endpoint, err := apiURL(cfg.addr, "/api/dns/resolve")
	if err != nil {
		return dnsResolveResult{}, err
	}
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return dnsResolveResult{}, fmt.Errorf("parse resolve url: %w", err)
	}
	query := reqURL.Query()
	query.Set("domain", domain)
	query.Set("type", qtype)
	reqURL.RawQuery = query.Encode()

	resp, err := doRequest(ctx, cfg, http.MethodGet, reqURL.String(), nil, http.StatusOK)
	if err != nil {
		return dnsResolveResult{}, err
	}
	defer resp.Body.Close()

	var result dnsResolveResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return dnsResolveResult{}, fmt.Errorf("decode DNS resolve result: %w", err)
	}
	return result, nil
}

func writeDNSResolve(w io.Writer, result dnsResolveResult, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "DOMAIN:{.Domain},QTYPE:{.QType},DECISION:{.Decision},UPSTREAM:{.Upstream},RESPONSE:{.Response}",
		WideColumnSpec:    "DOMAIN:{.Domain},QTYPE:{.QType},DECISION:{.Decision},RULE:{.Rule},UPSTREAM:{.Upstream},RCODE:{.RCode},LATENCY:{.Latency},CACHED:{.Cached},RESPONSE:{.Response},ANSWERS:{.Answers}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows([]dnsResolveRow{dnsResolveRowFromResult(result)}, flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func dnsResolveRowFromResult(result dnsResolveResult) dnsResolveRow {
	return dnsResolveRow{
		Domain:   result.Domain,
		QType:    result.QType,
		Decision: formatOptional(result.Decision),
		Upstream: formatOptional(result.Upstream),
		Response: formatOptional(result.Response),
		Rule:     formatOptional(result.Rule),
		RCode:    formatOptional(result.RCode),
		Latency:  latencyMS(result.Latency),
		Cached:   result.Cached,
		Answers:  formatOptional(formatDNSAnswers(result.Answers)),
	}
}

func formatDNSAnswers(answers []dnsResolveAnswer) string {
	if len(answers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(answers))
	for _, answer := range answers {
		ttl := ""
		if answer.TTL > 0 {
			ttl = fmt.Sprintf(" ttl=%d", answer.TTL)
		}
		value := strings.TrimSpace(answer.Value)
		if value == "" {
			value = "-"
		}
		parts = append(parts, fmt.Sprintf("%s %s%s", answer.Type, value, ttl))
	}
	return strings.Join(parts, "; ")
}
