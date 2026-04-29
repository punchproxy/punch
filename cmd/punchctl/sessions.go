package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thediveo/klo"
)

var errSessionNotFound = errors.New("session not found")

type sessionInfo struct {
	ID          string         `json:"id" yaml:"id"`
	Status      string         `json:"status" yaml:"status"`
	Domain      string         `json:"domain" yaml:"domain"`
	Destination string         `json:"destination" yaml:"destination"`
	Source      string         `json:"source" yaml:"source"`
	DstIP       string         `json:"dst_ip" yaml:"dst_ip"`
	DstPort     int            `json:"dst_port" yaml:"dst_port"`
	Protocol    string         `json:"protocol" yaml:"protocol"`
	Relay       string         `json:"relay" yaml:"relay"`
	Rule        string         `json:"rule" yaml:"rule"`
	Process     string         `json:"process,omitempty" yaml:"process,omitempty"`
	FakeIP      string         `json:"fake_ip,omitempty" yaml:"fake_ip,omitempty"`
	Upload      int64          `json:"upload_bytes" yaml:"upload_bytes"`
	Download    int64          `json:"download_bytes" yaml:"download_bytes"`
	Established time.Time      `json:"established" yaml:"established"`
	ClosedAt    time.Time      `json:"closed_at,omitempty" yaml:"closed_at,omitempty"`
	DurationMS  int64          `json:"duration_ms" yaml:"duration_ms"`
	Trace       []sessionTrace `json:"trace,omitempty" yaml:"trace,omitempty"`
}

type sessionTrace struct {
	At       time.Time `json:"at" yaml:"at"`
	OffsetMS int64     `json:"offset_ms" yaml:"offset_ms"`
	Message  string    `json:"message" yaml:"message"`
}

type sessionRow struct {
	ID          string     `json:"id" yaml:"id"`
	Established string     `json:"established" yaml:"established"`
	Destination string     `json:"destination" yaml:"destination"`
	Source      string     `json:"source" yaml:"source"`
	Protocol    string     `json:"protocol" yaml:"protocol"`
	Relay       string     `json:"relay" yaml:"relay"`
	Time        durationMS `json:"time_ms" yaml:"time_ms"`
	Download    byteSize   `json:"download_bytes" yaml:"download_bytes"`
	Upload      byteSize   `json:"upload_bytes" yaml:"upload_bytes"`
	Status      string     `json:"status" yaml:"status"`
}

func newSessionsCommand(cfg *commandConfig) *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Inspect recent sessions",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			sessions, err := fetchSessions(c.Context(), *cfg)
			if err != nil {
				return err
			}
			return writeSessions(c.OutOrStdout(), sessions, flags)
		},
	}
	addListFlags(cmd, &flags)
	cmd.AddCommand(newSessionGetCommand(cfg))
	cmd.AddCommand(newSessionTerminateCommand(cfg))
	return cmd
}

func newSessionGetCommand(cfg *commandConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "get ID",
		Short: "Show session details",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			session, err := fetchSession(c.Context(), *cfg, args[0])
			if err != nil {
				if errors.Is(err, errSessionNotFound) {
					return fmt.Errorf("session %q not found", args[0])
				}
				return err
			}
			return writeSessionDescribe(c.OutOrStdout(), session)
		},
	}
}

func newSessionTerminateCommand(cfg *commandConfig) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "terminate [ID]",
		Short: "Terminate active sessions",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("ID cannot be used with --all")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			if all {
				n, err := terminateAllSessions(c.Context(), *cfg)
				if err != nil {
					return err
				}
				fmt.Fprintf(c.OutOrStdout(), "%d session(s) terminated\n", n)
				return nil
			}
			if err := terminateSession(c.Context(), *cfg, args[0]); err != nil {
				if errors.Is(err, errSessionNotFound) {
					return fmt.Errorf("active session %q not found", args[0])
				}
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "session %q terminated\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "terminate all active sessions")
	return cmd
}

func fetchSessions(ctx context.Context, cfg commandConfig) ([]sessionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/sessions")
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sessions []sessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return sessions, nil
}

func fetchSession(ctx context.Context, cfg commandConfig, id string) (sessionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/sessions/"+url.PathEscape(id))
	if err != nil {
		return sessionInfo{}, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodGet, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return sessionInfo{}, errSessionNotFound
		}
		return sessionInfo{}, err
	}
	defer resp.Body.Close()
	var session sessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return sessionInfo{}, fmt.Errorf("decode session: %w", err)
	}
	return session, nil
}

func terminateSession(ctx context.Context, cfg commandConfig, id string) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/sessions/"+url.PathEscape(id))
	if err != nil {
		return err
	}
	resp, err := doRequest(ctx, cfg, http.MethodDelete, endpoint, nil, http.StatusOK)
	if err != nil {
		if errors.Is(err, errAPINotFound) {
			return errSessionNotFound
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func terminateAllSessions(ctx context.Context, cfg commandConfig) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	endpoint, err := apiURL(cfg.addr, "/api/sessions?all=true")
	if err != nil {
		return 0, err
	}
	resp, err := doRequest(ctx, cfg, http.MethodDelete, endpoint, nil, http.StatusOK)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var result struct {
		Terminated int `json:"terminated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode session termination: %w", err)
	}
	return result.Terminated, nil
}

func writeSessions(w io.Writer, sessions []sessionInfo, flags listFlags) error {
	if flags.output == "table" {
		flags.output = ""
	}
	printer, err := klo.PrinterFromFlag(flags.output, &klo.Specs{
		DefaultColumnSpec: "ID:{.ID},ESTABLISHED:{.Established},DESTINATION:{.Destination},SOURCE:{.Source},PROTOCOL:{.Protocol},RELAY:{.Relay},TIME:{.Time},DOWNLOAD:{.Download},UPLOAD:{.Upload},STATUS:{.Status}",
		WideColumnSpec:    "ID:{.ID},ESTABLISHED:{.Established},DESTINATION:{.Destination},SOURCE:{.Source},PROTOCOL:{.Protocol},RELAY:{.Relay},TIME:{.Time},DOWNLOAD:{.Download},UPLOAD:{.Upload},STATUS:{.Status}",
		GoTemplateArg:     flags.template,
	})
	if err != nil {
		return err
	}
	rows, err := prepareRows(sessionRows(sessions), flags.fieldSelector, flags.sortBy)
	if err != nil {
		return err
	}
	return printer.Fprint(w, rows)
}

func sessionRows(sessions []sessionInfo) []sessionRow {
	rows := make([]sessionRow, 0, len(sessions))
	for _, session := range sessions {
		rows = append(rows, sessionRow{
			ID:          session.ID,
			Established: formatSessionTime(session.Established),
			Destination: sessionListDestination(session),
			Source:      session.Source,
			Protocol:    session.Protocol,
			Relay:       session.Relay,
			Time:        durationMS(session.DurationMS),
			Download:    byteSize(session.Download),
			Upload:      byteSize(session.Upload),
			Status:      session.Status,
		})
	}
	return rows
}

func writeSessionDescribe(w io.Writer, session sessionInfo) error {
	fmt.Fprintf(w, "Destination:  %s\n", session.Destination)
	fmt.Fprintf(w, "Fake IP:      %s\n", formatOptional(session.FakeIP))
	fmt.Fprintf(w, "Source:       %s\n", formatOptional(session.Source))
	fmt.Fprintf(w, "Protocol:     %s\n", formatOptional(session.Protocol))
	fmt.Fprintf(w, "Relay:        %s\n", formatOptional(session.Relay))
	fmt.Fprintf(w, "Status:       %s\n", formatOptional(session.Status))
	fmt.Fprintf(w, "Established:  %s\n", formatSessionTime(session.Established))
	fmt.Fprintf(w, "Duration:     %s\n", formatDurationMS(session.DurationMS))
	fmt.Fprintf(w, "Download:     %s\n", formatBytes(session.Download))
	fmt.Fprintf(w, "Upload:       %s\n", formatBytes(session.Upload))
	fmt.Fprintln(w, "Trace:")
	if len(session.Trace) == 0 {
		fmt.Fprintln(w, "  -")
		return nil
	}
	for _, entry := range session.Trace {
		fmt.Fprintf(w, "[%s] %s\n", formatTraceOffset(entry.OffsetMS), entry.Message)
	}
	return nil
}

func sessionListDestination(session sessionInfo) string {
	if session.Domain != "" {
		return session.Domain
	}
	if session.Destination != "" {
		return strings.TrimSuffix(session.Destination, fmt.Sprintf(":%d", session.DstPort))
	}
	return session.DstIP
}

func formatSessionTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05.000")
}

func formatDurationMS(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return "0s"
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func formatTraceOffset(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	d := time.Duration(ms) * time.Millisecond
	minutes := int(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int(d / time.Second)
	d -= time.Duration(seconds) * time.Second
	millis := int(d / time.Millisecond)
	return fmt.Sprintf("%02d:%02d.%03d", minutes, seconds, millis)
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(bytes) / 1024
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}
