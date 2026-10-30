package p2p_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

type pollRoundTripper func(*http.Request) (*http.Response, error)

func (f pollRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type countingReadCloser struct {
	io.Reader
	n int
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += n
	return n, err
}

func (r *countingReadCloser) Close() error {
	return nil
}

func TestPollRelayBoundsResponseBeforeJSONDecode(t *testing.T) {
	t.Parallel()

	payload := append(
		[]byte(`{"req_id":"oversized","body":"`),
		bytes.Repeat([]byte("A"), 4*protocol.MaxRelayBodyBytes)...,
	)
	source := &countingReadCloser{Reader: bytes.NewReader(payload)}
	client := p2p.NewHTTPPeerClient(pollRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       source,
		}, nil
	}), "", nil)

	_, err := client.PollRelay(context.Background(), "https://sponsor.invalid", "peer")
	require.Error(t, err)
	require.LessOrEqual(t, source.n, 2*protocol.MaxRelayBodyBytes+1,
		"poll decoding must stop before reading an unbounded base64 envelope")
}

func TestPollRelayEscapesQueryParameter(t *testing.T) {
	t.Parallel()

	specialPeerID := "peer+special/name with spaces&symbols"
	var capturedQuery string
	var capturedRawQuery string

	client := p2p.NewHTTPPeerClient(pollRoundTripper(func(req *http.Request) (*http.Response, error) {
		capturedQuery = req.URL.Query().Get("id")
		capturedRawQuery = req.URL.RawQuery
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}), "", nil)

	_, err := client.PollRelay(context.Background(), "https://sponsor.invalid", specialPeerID)
	require.NoError(t, err)
	require.Equal(t, specialPeerID, capturedQuery)
	require.Contains(t, capturedRawQuery, "id=peer%2Bspecial%2Fname+with+spaces%26symbols")
}
