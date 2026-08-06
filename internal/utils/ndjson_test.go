package utils_test

import (
	"bytes"
	"context"
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

func TestForEachNDJSONSkipsBrokenLines(t *testing.T) {
	t.Parallel()
	r := strings.NewReader("{\"ok\":true}\nnot-json\n{\"b\":2}\n\n")
	var got []map[string]any
	require.NoError(t, utils.ForEachNDJSON(r, func(chunk map[string]any) error {
		got = append(got, chunk)
		return nil
	}))
	require.Len(t, got, 2)
	require.Equal(t, true, got[0]["ok"])
	require.Equal(t, float64(2), got[1]["b"])
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
