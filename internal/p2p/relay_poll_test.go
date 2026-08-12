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
