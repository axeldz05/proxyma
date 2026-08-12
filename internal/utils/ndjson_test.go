package utils_test

import (
	"bytes"
	"context"
	"errors"
	"proxyma/internal/utils"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteNDJSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, utils.WriteNDJSON(&buf, map[string]any{"a": 1}))
	require.Equal(t, "{\"a\":1}\n", buf.String())
}

func TestForEachNDJSONRejectsBrokenLines(t *testing.T) {
	t.Parallel()
	r := strings.NewReader("{\"ok\":true}\nnot-json\n{\"b\":2}\n\n")
	var got []map[string]any
	err := utils.ForEachNDJSON(r, func(chunk map[string]any) error {
		got = append(got, chunk)
		return nil
	})
	require.Error(t, err)
	require.Len(t, got, 1)
	require.Equal(t, true, got[0]["ok"])
}

func TestPumpJSONEncodeDecode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	in := make(chan map[string]any, 2)
	in <- map[string]any{"n": 1}
	in <- map[string]any{"n": 2}
	close(in)

	var buf bytes.Buffer
	require.NoError(t, utils.PumpJSONEncode(ctx, &buf, in))

	out := make(chan map[string]any, 4)
	require.NoError(t, utils.PumpJSONDecode(ctx, &buf, out))
	close(out)

	var items []map[string]any
	for item := range out {
		items = append(items, item)
	}
	require.Len(t, items, 2)
}

func TestPumpJSONEncodeContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := make(chan map[string]any)
	err := utils.PumpJSONEncode(ctx, &bytes.Buffer{}, in)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPumpJSONDecodeDoesNotTurnCanceledEOFIntoSuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := utils.PumpJSONDecode(ctx, strings.NewReader(""), make(chan map[string]any, 1))
	require.ErrorIs(t, err, context.Canceled)
}

func TestPumpJSONDecodeRejectsMalformedAndNonObjectChunks(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"{\"ok\":true}\n{bad\n",
		"null\n",
		"[]\n",
	} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			out := make(chan map[string]any, 2)
			err := utils.PumpJSONDecode(context.Background(), strings.NewReader(input), out)
			require.Error(t, err)
		})
	}
}

func TestScanNDJSONFrameLimitBoundary(t *testing.T) {
	t.Parallel()

	const limit = 256
	prefix := `{"image":"`
	suffix := `"}`
	exact := prefix + strings.Repeat("a", limit-len(prefix)-len(suffix)) + suffix
	require.Len(t, exact, limit)

	var frames [][]byte
	err := utils.ScanNDJSONWithLimit(strings.NewReader(exact+"\n"), limit, func(frame []byte) bool {
		frames = append(frames, append([]byte(nil), frame...))
		return true
	})
	require.NoError(t, err)
	require.Equal(t, [][]byte{[]byte(exact)}, frames)

	oversized := prefix + strings.Repeat("a", limit-len(prefix)-len(suffix)+1) + suffix
	err = utils.ScanNDJSONWithLimit(strings.NewReader(oversized+"\n"), limit, func([]byte) bool {
		t.Fatal("oversized frame reached callback")
		return false
	})
	require.ErrorIs(t, err, utils.ErrNDJSONFrameTooLarge)
}

func TestDefaultNDJSONFrameCapSupportsImageChunks(t *testing.T) {
	t.Parallel()

	if utils.MaxNDJSONFrameBytes < 8<<20 {
		t.Fatalf("NDJSON cap = %d, want at least 8 MiB for screen/image chunks", utils.MaxNDJSONFrameBytes)
	}
	imageChunk := map[string]any{
		"image_base64": strings.Repeat("a", 8<<20),
	}
	var encoded bytes.Buffer
	require.NoError(t, utils.WriteNDJSON(&encoded, imageChunk))

	tooLarge := map[string]any{
		"image_base64": strings.Repeat("a", utils.MaxNDJSONFrameBytes),
	}
	err := utils.WriteNDJSON(&bytes.Buffer{}, tooLarge)
	require.True(t, errors.Is(err, utils.ErrNDJSONFrameTooLarge), err)
}
