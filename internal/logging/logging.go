package logging

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

const ringLimit = 500

type Entry struct {
	Seq  uint64 `json:"seq"`
	Line string `json:"line"`
}

var (
	levelVar slog.LevelVar

	mu     sync.Mutex
	next   uint64 = 1
	ring   []Entry
	subs   = map[chan string]struct{}{}
	prefix []byte
)

func Setup(level, file string) {
	SetLevel(level)

	var dest io.Writer = os.Stderr
	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot open log file %s: %v\n", file, err)
		} else {
			dest = f
		}
	}

	opts := &slog.HandlerOptions{Level: &levelVar}
	handler := slog.NewTextHandler(io.MultiWriter(dest, streamWriter{}), opts)
	slog.SetDefault(slog.New(handler))
}

func SetLevel(level string) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		levelVar.Set(slog.LevelDebug)
	case "warn", "warning":
		levelVar.Set(slog.LevelWarn)
	case "error":
		levelVar.Set(slog.LevelError)
	default:
		levelVar.Set(slog.LevelInfo)
	}
}

func Subscribe() (<-chan string, func()) {
	ch := make(chan string, ringLimit+128)

	mu.Lock()
	for _, entry := range ring {
		ch <- entry.Line
	}
	subs[ch] = struct{}{}
	mu.Unlock()

	return ch, func() {
		mu.Lock()
		delete(subs, ch)
		close(ch)
		mu.Unlock()
	}
}

func SnapshotSince(seq uint64) ([]Entry, uint64) {
	mu.Lock()
	defer mu.Unlock()

	entries := make([]Entry, 0, len(ring))
	for _, entry := range ring {
		if entry.Seq > seq {
			entries = append(entries, entry)
		}
	}
	return entries, next - 1
}

type streamWriter struct{}

func (streamWriter) Write(p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()

	data := append(prefix, p...)
	lines := bytes.Split(data, []byte{'\n'})
	prefix = append(prefix[:0], lines[len(lines)-1]...)

	for _, raw := range lines[:len(lines)-1] {
		line := string(bytes.TrimRight(raw, "\r"))
		if line == "" {
			continue
		}
		entry := Entry{Seq: next, Line: line}
		next++
		ring = append(ring, entry)
		if len(ring) > ringLimit {
			copy(ring, ring[len(ring)-ringLimit:])
			ring = ring[:ringLimit]
		}
		for ch := range subs {
			select {
			case ch <- entry.Line:
			default:
			}
		}
	}
	return len(p), nil
}
