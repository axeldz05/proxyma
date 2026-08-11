package compute

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"
)

// minimalJPEG is a tiny valid JPEG (SOI … EOI) used as a fake screen frame.
var minimalJPEG = mustDecodeB64("/9j/4AAQSkZJRgABAQEASABIAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAv/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k=")

func mustDecodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// BuildScreenHandler streams media frames as NDJSON-shaped maps:
// {"n": <int>, "frame_b64": "<jpeg>"}.
//
// source "fake" (or empty): in-process JPEG generator.
// Optional first in-message {"frames": N} limits count; otherwise streams until ctx cancel.
func BuildScreenHandler(source string, timeout time.Duration) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if out == nil {
			return nil, fmt.Errorf("screen stream requires an output channel")
		}
		defer close(out)

		ctx, cancel := withHandlerTimeout(ctx, timeout)
		defer cancel()

		switch source {
		case "", "fake":
			return pumpFakeScreenFrames(ctx, in, out, payload)
		default:
			return nil, fmt.Errorf("screen source %q not supported (use fake)", source)
		}
	}
}

func pumpFakeScreenFrames(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
	maxFrames := 0 // 0 = until cancel
	if payload != nil {
		if n, ok := asPositiveInt(payload["frames"]); ok {
			maxFrames = n
		}
	}
	if in != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg, ok := <-in:
			if ok && msg != nil {
				if n, ok := asPositiveInt(msg["frames"]); ok {
					maxFrames = n
				}
			}
		default:
		}
	}

	interval := 50 * time.Millisecond
	frameB64 := base64.StdEncoding.EncodeToString(minimalJPEG)
	for i := 1; maxFrames == 0 || i <= maxFrames; i++ {
		chunk := map[string]any{
			"n":         float64(i),
			"frame_b64": frameB64,
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case out <- chunk:
		}
		if maxFrames > 0 && i >= maxFrames {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
	return nil, nil
}

func asPositiveInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 1 {
			return int(n), true
		}
	case int:
		if n >= 1 {
			return n, true
		}
	case int64:
		if n >= 1 {
			return int(n), true
		}
	}
	return 0, false
}
