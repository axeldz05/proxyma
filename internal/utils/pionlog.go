package utils

import (
	"fmt"
	"log/slog"

	"github.com/pion/logging"
)

// SlogLike is the minimal logging surface shared by *slog.Logger and
// p2p.Logger, so pion components can log through whichever logger the caller
// owns.
type SlogLike interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// pionLeveledLogger adapts a SlogLike logger to pion's LeveledLogger so
// pion/turn components log through the node logger instead of stderr.
type pionLeveledLogger struct {
	logger SlogLike
}

func (p pionLeveledLogger) Trace(msg string) { p.logger.Debug(msg) }
func (p pionLeveledLogger) Tracef(format string, args ...any) {
	p.logger.Debug(fmt.Sprintf(format, args...))
}
func (p pionLeveledLogger) Debug(msg string) { p.logger.Debug(msg) }
func (p pionLeveledLogger) Debugf(format string, args ...any) {
	p.logger.Debug(fmt.Sprintf(format, args...))
}
func (p pionLeveledLogger) Info(msg string) { p.logger.Info(msg) }
func (p pionLeveledLogger) Infof(format string, args ...any) {
	p.logger.Info(fmt.Sprintf(format, args...))
}
func (p pionLeveledLogger) Warn(msg string) { p.logger.Warn(msg) }
func (p pionLeveledLogger) Warnf(format string, args ...any) {
	p.logger.Warn(fmt.Sprintf(format, args...))
}
func (p pionLeveledLogger) Error(msg string) { p.logger.Error(msg) }
func (p pionLeveledLogger) Errorf(format string, args ...any) {
	p.logger.Error(fmt.Sprintf(format, args...))
}

type pionLoggerFactory struct{ base SlogLike }

// PionLoggerFactory returns a pion logging.LoggerFactory backed by l.
func PionLoggerFactory(l SlogLike) logging.LoggerFactory {
	if l == nil {
		l = slog.Default()
	}
	return pionLoggerFactory{base: l}
}

func (f pionLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	return pionLeveledLogger{logger: f.base}
}
