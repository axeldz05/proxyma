package protocol

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLevelFromLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "uppercase ERROR", input: "2026-10-30T10:00:00Z level=ERROR msg=boom", expected: "ERROR"},
		{name: "lowercase error", input: "2026-10-30T10:00:00Z level=error msg=boom", expected: "ERROR"},
		{name: "uppercase WARN", input: "2026-10-30T10:00:00Z level=WARN msg=careful", expected: "WARN"},
		{name: "lowercase warn", input: "2026-10-30T10:00:00Z level=warn msg=careful", expected: "WARN"},
		{name: "uppercase DEBUG", input: "2026-10-30T10:00:00Z level=DEBUG msg=tracing", expected: "DEBUG"},
		{name: "lowercase debug", input: "2026-10-30T10:00:00Z level=debug msg=tracing", expected: "DEBUG"},
		{name: "uppercase INFO", input: "2026-10-30T10:00:00Z level=INFO msg=hello", expected: "INFO"},
		{name: "lowercase info", input: "2026-10-30T10:00:00Z level=info msg=hello", expected: "INFO"},
		{name: "empty string", input: "", expected: "INFO"},
		{name: "plain string no level", input: "just some random output text without any level token", expected: "INFO"},
		{name: "malformed level empty value", input: "level=", expected: "INFO"},
		{name: "malformed level double equal", input: "level==ERROR", expected: "INFO"},
		{name: "malformed level plural", input: "levels=ERROR", expected: "INFO"},
		{name: "multi-line with error on second line", input: "header line\nsome stack trace level=ERROR\nfooter line", expected: "ERROR"},
		{name: "multi-line with warn on first line", input: "warning line level=WARN\nsome other line", expected: "WARN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := levelFromLine(tc.input)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestLogWriterHeavyConcurrentWritesAndWrapping(t *testing.T) {
	LogBufferMu.Lock()
	saved := LogBuffer
	LogBuffer = nil
	LogBufferMu.Unlock()
	defer func() {
		LogBufferMu.Lock()
		LogBuffer = saved
		LogBufferMu.Unlock()
	}()

	writer := &LogWriter{Stdout: io.Discard}

	var wg sync.WaitGroup
	const goroutines = 25
	const writesPerGoroutine = 80

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				line := fmt.Sprintf("goroutine %d write %d level=DEBUG message\n", gid, i)
				n, err := writer.Write([]byte(line))
				require.NoError(t, err)
				require.Equal(t, len(line), n)
			}
		}(g)
	}
	wg.Wait()

	LogBufferMu.RLock()
	defer LogBufferMu.RUnlock()

	require.Equal(t, logRingCapacity, len(LogBuffer), "buffer should wrap and cap exactly at logRingCapacity")
	for _, rec := range LogBuffer {
		require.NotEmpty(t, rec.Timestamp)
		require.Equal(t, "DEBUG", rec.Level)
		require.NotEmpty(t, rec.Message)
	}
}

func TestLogWriterPropagatesStdout(t *testing.T) {
	LogBufferMu.Lock()
	saved := LogBuffer
	LogBuffer = nil
	LogBufferMu.Unlock()
	defer func() {
		LogBufferMu.Lock()
		LogBuffer = saved
		LogBufferMu.Unlock()
	}()

	var buf bytes.Buffer
	writer := &LogWriter{Stdout: &buf}

	msg := "test log entry level=WARN something\n"
	n, err := writer.Write([]byte(msg))
	require.NoError(t, err)
	require.Equal(t, len(msg), n)
	require.Equal(t, msg, buf.String())

	LogBufferMu.RLock()
	defer LogBufferMu.RUnlock()
	require.Len(t, LogBuffer, 1)
	require.Equal(t, "WARN", LogBuffer[0].Level)
	require.Equal(t, "test log entry level=WARN something", LogBuffer[0].Message)
}

func TestLogWriterNilStdout(t *testing.T) {
	LogBufferMu.Lock()
	saved := LogBuffer
	LogBuffer = nil
	LogBufferMu.Unlock()
	defer func() {
		LogBufferMu.Lock()
		LogBuffer = saved
		LogBufferMu.Unlock()
	}()

	writer := &LogWriter{Stdout: nil}
	msg := "nil stdout write level=INFO ok\n"
	n, err := writer.Write([]byte(msg))
	require.NoError(t, err)
	require.Equal(t, len(msg), n)

	LogBufferMu.RLock()
	defer LogBufferMu.RUnlock()
	require.Len(t, LogBuffer, 1)
	require.Equal(t, "INFO", LogBuffer[0].Level)
}

