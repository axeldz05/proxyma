package utils_test

import (
	"io"
	"proxyma/internal/utils"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountingReadCloser(t *testing.T) {
	t.Parallel()
	var total int
	rc := &utils.CountingReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader("hello world")),
		OnRead:     func(n int) { total += n },
	}
	buf := make([]byte, 5)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, 5, total)

	rest, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, " world", string(rest))
	require.Equal(t, 11, total)
	require.NoError(t, rc.Close())
}

func TestCountingReadCloserNilCallback(t *testing.T) {
	t.Parallel()
	rc := &utils.CountingReadCloser{ReadCloser: io.NopCloser(strings.NewReader("x"))}
	buf := make([]byte, 1)
	n, err := rc.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
