package tun

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type singLogger struct{}

func (singLogger) Trace(args ...any) { slog.Debug(formatSingLogArgs(args...)) }
func (singLogger) Debug(args ...any) { slog.Debug(formatSingLogArgs(args...)) }
func (singLogger) Info(args ...any)  { slog.Info(formatSingLogArgs(args...)) }
func (singLogger) Warn(args ...any)  { slog.Warn(formatSingLogArgs(args...)) }
func (singLogger) Error(args ...any) { slog.Error(formatSingLogArgs(args...)) }
func (singLogger) Fatal(args ...any) { slog.Error(formatSingLogArgs(args...)) }
func (singLogger) Panic(args ...any) { slog.Error(formatSingLogArgs(args...)) }

func (l singLogger) TraceContext(_ context.Context, args ...any) { l.Trace(args...) }
func (l singLogger) DebugContext(_ context.Context, args ...any) { l.Debug(args...) }
func (l singLogger) InfoContext(_ context.Context, args ...any)  { l.Info(args...) }
func (l singLogger) WarnContext(_ context.Context, args ...any)  { l.Warn(args...) }
func (l singLogger) ErrorContext(_ context.Context, args ...any) { l.Error(args...) }
func (l singLogger) FatalContext(_ context.Context, args ...any) { l.Fatal(args...) }
func (l singLogger) PanicContext(_ context.Context, args ...any) { l.Panic(args...) }

func formatSingLogArgs(args ...any) string {
	if len(args) == 0 {
		return ""
	}
	var builder strings.Builder
	for i, arg := range args {
		if i > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(fmt.Sprint(arg))
	}
	return builder.String()
}
