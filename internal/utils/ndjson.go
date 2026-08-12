package utils

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxNDJSONFrameBytes = 16 << 20

var ErrNDJSONFrameTooLarge = errors.New("NDJSON frame exceeds configured limit")

// WriteNDJSON marshals v as one NDJSON line (JSON + newline) to w (L1).
func WriteNDJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxNDJSONFrameBytes {
		return fmt.Errorf("%w: %d > %d bytes", ErrNDJSONFrameTooLarge, len(b), MaxNDJSONFrameBytes)
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// PumpJSONEncode writes channel items as NDJSON to w until in closes or ctx cancels (L1).
func PumpJSONEncode(ctx context.Context, w io.Writer, in <-chan map[string]any) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-in:
			if !ok {
				return nil
			}
			if err := WriteNDJSON(w, item); err != nil {
				return err
			}
		}
	}
}

// PumpJSONDecode reads NDJSON from r into out until EOF or ctx cancel (L1).
func PumpJSONDecode(ctx context.Context, r io.Reader, out chan<- map[string]any) error {
	var pumpErr error
	err := ScanNDJSON(r, func(line []byte) bool {
		var result map[string]any
		if err := json.Unmarshal(line, &result); err != nil {
			pumpErr = err
			return false
		}
		if result == nil {
			pumpErr = fmt.Errorf("NDJSON chunk must be a JSON object")
			return false
		}
		select {
		case <-ctx.Done():
			pumpErr = ctx.Err()
			return false
		case out <- result:
			return true
		}
	})
	if pumpErr != nil {
		return pumpErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// ForEachNDJSON scans r line-by-line, unmarshals each non-empty line, and calls fn (L1).
func ForEachNDJSON(r io.Reader, fn func(chunk map[string]any) error) error {
	var callbackErr error
	var decodeErr error
	err := ScanNDJSON(r, func(line []byte) bool {
		var chunk map[string]any
		if err := json.Unmarshal(line, &chunk); err != nil {
			decodeErr = fmt.Errorf("invalid NDJSON chunk: %w", err)
			return false
		}
		if chunk == nil {
			decodeErr = fmt.Errorf("NDJSON chunk must be a JSON object")
			return false
		}
		if err := fn(chunk); err != nil {
			callbackErr = err
			return false
		}
		return true
	})
	if callbackErr != nil {
		return callbackErr
	}
	if decodeErr != nil {
		return decodeErr
	}
	return err
}

// ScanNDJSON scans non-empty NDJSON lines; onLine returns false to stop (L1).
func ScanNDJSON(r io.Reader, onLine func(line []byte) bool) error {
	return ScanNDJSONWithLimit(r, MaxNDJSONFrameBytes, onLine)
}

// ScanNDJSONWithLimit scans frames up to maxFrameBytes, excluding the newline.
func ScanNDJSONWithLimit(r io.Reader, maxFrameBytes int, onLine func(line []byte) bool) error {
	if maxFrameBytes <= 0 {
		return fmt.Errorf("invalid NDJSON frame limit %d", maxFrameBytes)
	}
	reader := bufio.NewReaderSize(r, min(maxFrameBytes+1, 64<<10))
	line := make([]byte, 0, min(maxFrameBytes, 64<<10))
	for {
		fragment, err := reader.ReadSlice('\n')
		line = append(line, fragment...)
		if len(line) > maxFrameBytes+1 ||
			(len(line) == maxFrameBytes+1 && line[len(line)-1] != '\n') {
			return fmt.Errorf("%w: maximum is %d bytes", ErrNDJSONFrameTooLarge, maxFrameBytes)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}
		if len(line) > maxFrameBytes {
			return fmt.Errorf("%w: maximum is %d bytes", ErrNDJSONFrameTooLarge, maxFrameBytes)
		}
		if len(line) == 0 {
			if errors.Is(err, io.EOF) {
				return nil
			}
			line = line[:0]
			continue
		}
		if !onLine(line) {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		line = line[:0]
	}
}
