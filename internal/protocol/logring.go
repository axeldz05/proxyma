package protocol

import (
	"io"
	"strings"
	"sync"
	"time"
)

// logRingCapacity is how many recent lines the in-memory buffer keeps for
// `proxyma logs` and the mobile UI.
const logRingCapacity = 1000

type LogRecord struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"` // "INFO", "WARN", "ERROR", "DEBUG"
	Message   string `json:"message"`
}

// LogBuffer is process-global on purpose: one node per process, and the UI reads
// it through LocalLogs.
var (
	LogBuffer   []LogRecord
	LogBufferMu sync.RWMutex
)

// LogWriter tees slog output to Stdout and to the in-memory ring.
type LogWriter struct {
	Stdout io.Writer
}

func (w *LogWriter) Write(p []byte) (n int, err error) {
	n, err = w.Stdout.Write(p)
	line := string(p)

	LogBufferMu.Lock()
	LogBuffer = append(LogBuffer, LogRecord{
		Timestamp: time.Now().Format("15:04:05"),
		Level:     levelFromLine(line),
		Message:   strings.TrimSpace(line),
	})
	if len(LogBuffer) > logRingCapacity {
		LogBuffer = LogBuffer[len(LogBuffer)-logRingCapacity:]
	}
	LogBufferMu.Unlock()

	return n, err
}

// levelFromLine recovers the slog level from an already-formatted line.
func levelFromLine(line string) string {
	for _, level := range []string{"ERROR", "WARN", "DEBUG"} {
		if strings.Contains(line, "level="+level) || strings.Contains(line, "level="+strings.ToLower(level)) {
			return level
		}
	}
	return "INFO"
}
