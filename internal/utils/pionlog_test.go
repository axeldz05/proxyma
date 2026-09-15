package utils

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

type recordingHandler struct {
	lines *[]string
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.lines = append(*h.lines, r.Level.String()+" "+r.Message)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

func TestPionLeveledLoggerRoutesLevels(t *testing.T) {
	var lines []string
	base := slog.New(recordingHandler{lines: &lines})
	logger := PionLoggerFactory(base).NewLogger("turn")

	logger.Trace("t")
	logger.Tracef("tf %d", 1)
	logger.Debug("d")
	logger.Debugf("df %d", 2)
	logger.Info("i")
	logger.Infof("if %d", 3)
	logger.Warn("w")
	logger.Warnf("wf %d", 4)
	logger.Error("e")
	logger.Errorf("ef %d", 5)

	joined := strings.Join(lines, "\n")
	for _, want := range []string{"DEBUG t", "DEBUG tf 1", "DEBUG df 2", "INFO i", "INFO if 3", "WARN w", "WARN wf 4", "ERROR e", "ERROR ef 5"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log lines %q missing %q", joined, want)
		}
	}
}

func TestPionLoggerFactoryDefaultsToSlog(t *testing.T) {
	// A nil SlogLike must fall back to slog.Default instead of panicking.
	logger := PionLoggerFactory(nil).NewLogger("scope")
	if logger == nil {
		t.Fatal("expected non-nil leveled logger for nil base")
	}
	// Smoke: methods must not panic on the default logger.
	logger.Debug("smoke")
	logger.Errorf("smoke %s", "fmt")
}
