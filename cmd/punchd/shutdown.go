package main

import (
	"log/slog"
	"os"
)

// runShutdown restores networking before stopping services. A further signal
// skips every remaining wait, including network restoration. The caller must
// use os.Exit when forced is true so deferred cleanup cannot block exit.
func runShutdown(signals <-chan os.Signal, restoreNetwork func() error, stopServices func()) (forced bool) {
	done := make(chan struct{})
	go func() {
		if err := restoreNetwork(); err != nil {
			slog.Error("system network cleanup failed", "error", err)
		} else {
			slog.Info("system DNS and routes restored; TUN device closed")
		}
		stopServices()
		close(done)
	}()
	select {
	case <-done:
		return false
	case <-signals:
		// Do not even wait for a log write: the logger may be blocked too.
		return true
	}
}
