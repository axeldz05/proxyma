package utils

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// WriteNDJSON marshals v as one NDJSON line (JSON + newline) to w (L1).
func WriteNDJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// PumpJSONEncode writes channel items as NDJSON to w until in closes or ctx cancels (L1).
func PumpJSONEncode(ctx context.Context, w io.Writer, in <-chan map[string]any) error {
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-in:
			if !ok {
				return nil
			}
			if err := encoder.Encode(item); err != nil {
				return err
			}
		}
	}
}

// PumpJSONDecode reads NDJSON from r into out until EOF or ctx cancel (L1).
func PumpJSONDecode(ctx context.Context, r io.Reader, out chan<- map[string]any) error {
	decoder := json.NewDecoder(r)
	for {
		var result map[string]any
		if err := decoder.Decode(&result); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- result:
		}
	}
}

// ForEachNDJSON scans r line-by-line, unmarshals each non-empty line, and calls fn (L1).
func ForEachNDJSON(r io.Reader, fn func(chunk map[string]any) error) error {
	var fnErr error
	err := ScanNDJSON(r, func(line []byte) bool {
		var chunk map[string]any
		if err := json.Unmarshal(line, &chunk); err != nil {
			return true
		}
		if err := fn(chunk); err != nil {
			fnErr = err
			return false
		}
		return true
	})
	if fnErr != nil {
		return fnErr
	}
	return err
}

// ScanNDJSON scans non-empty NDJSON lines; onLine returns false to stop (L1).
func ScanNDJSON(r io.Reader, onLine func(line []byte) bool) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		cp := append([]byte(nil), line...)
		if !onLine(cp) {
			return nil
		}
	}
	return scanner.Err()
}
