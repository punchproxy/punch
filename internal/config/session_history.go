package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/punchproxy/punch/internal/session"
)

// sessionHistoryModel spills closed sessions out of the manager's memory.
// History is per-run: punchd clears the table on startup, so session IDs only
// collide if a previous run's rows survive — the upsert below absorbs that.
type sessionHistoryModel struct {
	Seq            int64  `gorm:"column:seq;primaryKey;autoIncrement"`
	SessionID      string `gorm:"column:session_id;uniqueIndex;not null"`
	Status         string `gorm:"column:status;not null"`
	Domain         string `gorm:"column:domain;not null"`
	Source         string `gorm:"column:source;not null"`
	DstIP          string `gorm:"column:dst_ip;not null"`
	DstPort        int    `gorm:"column:dst_port;not null"`
	Protocol       string `gorm:"column:protocol;not null"`
	Relay          string `gorm:"column:relay;not null"`
	Rule           string `gorm:"column:rule;not null"`
	Process        string `gorm:"column:process;not null"`
	FakeIP         string `gorm:"column:fake_ip;not null"`
	Upload         int64  `gorm:"column:upload;not null"`
	Download       int64  `gorm:"column:download;not null"`
	StartNS        int64  `gorm:"column:start_ns;not null"`
	EndNS          int64  `gorm:"column:end_ns;not null"`
	DNSRequestedNS int64  `gorm:"column:dns_requested_ns;not null"`
	CloseReason    string `gorm:"column:close_reason;not null"`
	Trace          []byte `gorm:"column:trace"`
}

func (sessionHistoryModel) TableName() string { return "session_history" }

// AppendClosedSession implements session.HistoryStore.
func (s *Store) AppendClosedSession(rec session.ClosedRecord, limit int) error {
	row, err := sessionHistoryRow(rec)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "session_id"}},
			UpdateAll: true,
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("insert session history: %w", err)
		}
		if limit > 0 {
			if err := tx.Exec(
				"DELETE FROM session_history WHERE seq NOT IN (SELECT seq FROM session_history ORDER BY seq DESC LIMIT ?)",
				limit,
			).Error; err != nil {
				return fmt.Errorf("prune session history: %w", err)
			}
		}
		return nil
	})
}

// ListClosedSessions implements session.HistoryStore. Traces are left nil to
// keep list reads cheap; GetClosedSession returns them.
func (s *Store) ListClosedSessions(limit int) ([]session.ClosedRecord, error) {
	var rows []sessionHistoryModel
	q := s.db.Order("seq DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list session history: %w", err)
	}
	out := make([]session.ClosedRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionHistoryRecord(row, false))
	}
	return out, nil
}

// GetClosedSession implements session.HistoryStore.
func (s *Store) GetClosedSession(id string) (session.ClosedRecord, bool, error) {
	var row sessionHistoryModel
	err := s.db.First(&row, "session_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return session.ClosedRecord{}, false, nil
	}
	if err != nil {
		return session.ClosedRecord{}, false, fmt.Errorf("get session history %s: %w", id, err)
	}
	return sessionHistoryRecord(row, true), true, nil
}

// ClearClosedSessions implements session.HistoryStore.
func (s *Store) ClearClosedSessions() (int, error) {
	result := s.db.Exec("DELETE FROM session_history")
	if result.Error != nil {
		return 0, fmt.Errorf("clear session history: %w", result.Error)
	}
	return int(result.RowsAffected), nil
}

func sessionHistoryRow(rec session.ClosedRecord) (sessionHistoryModel, error) {
	trace, err := json.Marshal(rec.Trace)
	if err != nil {
		return sessionHistoryModel{}, fmt.Errorf("marshal session trace: %w", err)
	}
	return sessionHistoryModel{
		SessionID:      rec.ID,
		Status:         string(rec.Status),
		Domain:         rec.Domain,
		Source:         rec.Source,
		DstIP:          rec.DstIP,
		DstPort:        rec.DstPort,
		Protocol:       rec.Protocol,
		Relay:          rec.Relay,
		Rule:           rec.Rule,
		Process:        rec.Process,
		FakeIP:         rec.FakeIP,
		Upload:         rec.Upload,
		Download:       rec.Download,
		StartNS:        timeToNS(rec.StartTime),
		EndNS:          timeToNS(rec.EndTime),
		DNSRequestedNS: timeToNS(rec.DNSRequestedAt),
		CloseReason:    rec.CloseReason,
		Trace:          trace,
	}, nil
}

func sessionHistoryRecord(row sessionHistoryModel, withTrace bool) session.ClosedRecord {
	rec := session.ClosedRecord{
		ID:             row.SessionID,
		Status:         session.Status(row.Status),
		Domain:         row.Domain,
		Source:         row.Source,
		DstIP:          row.DstIP,
		DstPort:        row.DstPort,
		Protocol:       row.Protocol,
		Relay:          row.Relay,
		Rule:           row.Rule,
		Process:        row.Process,
		FakeIP:         row.FakeIP,
		Upload:         row.Upload,
		Download:       row.Download,
		StartTime:      nsToTime(row.StartNS),
		EndTime:        nsToTime(row.EndNS),
		DNSRequestedAt: nsToTime(row.DNSRequestedNS),
		CloseReason:    row.CloseReason,
	}
	if withTrace && len(row.Trace) > 0 {
		// A decode failure only loses the trace detail, not the session row.
		_ = json.Unmarshal(row.Trace, &rec.Trace)
	}
	return rec
}

func timeToNS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func nsToTime(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}
