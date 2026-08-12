package compute

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestScriptStreamReturnsSuccessOnlyAfterCleanExit(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`while IFS= read -r line; do printf '%s\n' "$line"; done`)
	in := make(chan map[string]any, 1)
	in <- map[string]any{"n": 1}
	close(in)
	out := make(chan map[string]any, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handler.ExecuteStream(ctx, in, out); err != nil {
		t.Fatalf("clean script stream: %v", err)
	}
	chunk, ok := <-out
	if !ok || chunk["n"] != float64(1) {
		t.Fatalf("stream chunk = %#v, open=%v", chunk, ok)
	}
}

func TestScriptStreamPropagatesEncoderFailure(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`while :; do :; done`)
	in := make(chan map[string]any, 1)
	in <- map[string]any{"unsupported": make(chan int)}
	close(in)
	out := make(chan map[string]any, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := handler.ExecuteStream(ctx, in, out); err == nil {
		t.Fatal("script stream ignored encoder failure")
	}
	if ctx.Err() != nil {
		t.Fatal("encoder failure was not propagated until the outer timeout")
	}
}

func TestScriptStreamPropagatesMalformedOutput(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`printf '%s\n' '{"n":1}' '{bad'`)
	in := make(chan map[string]any)
	close(in)
	out := make(chan map[string]any, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := handler.ExecuteStream(ctx, in, out)
	if err == nil {
		t.Fatal("script stream ignored malformed NDJSON")
	}
	chunk, ok := <-out
	if !ok || chunk["n"] != float64(1) {
		t.Fatalf("valid prefix chunk = %#v, open=%v", chunk, ok)
	}
}

func TestScriptStreamPropagatesCommandWaitFailureAfterChunks(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`printf '%s\n' '{"n":1}'; exit 7`)
	in := make(chan map[string]any)
	close(in)
	out := make(chan map[string]any, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := handler.ExecuteStream(ctx, in, out)
	if err == nil {
		t.Fatal("script stream ignored non-zero command exit")
	}
	chunk, ok := <-out
	if !ok || chunk["n"] != float64(1) {
		t.Fatalf("stream chunk = %#v, open=%v", chunk, ok)
	}
}

func TestScriptStreamDoesNotWaitForOpenInputAfterOutputEnds(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`exec 1>&-; cat >/dev/null`)
	in := make(chan map[string]any)
	out := make(chan map[string]any)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- handler.ExecuteStream(ctx, in, out)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("script stream after output EOF: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("script stream deadlocked waiting for caller to close input")
	}
}

func TestScriptStreamClosesBlockedStdinWriteAfterProcessExit(t *testing.T) {
	t.Parallel()

	handler := BuildScriptHandler(`exec 0<&-; exec 1>&-; exit 0`)
	in := make(chan map[string]any, 1)
	in <- map[string]any{"image_base64": strings.Repeat("a", 8<<20)}
	out := make(chan map[string]any)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- handler.ExecuteStream(ctx, in, out)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("script stream after process exit: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("blocked stdin write did not unblock after stdout/process exit")
	}
}
