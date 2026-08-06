package compute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"proxyma/internal/p2p"
	"proxyma/internal/utils"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

// newHostOnlyPeerConnection creates a PeerConnection with UDP4 host candidates only (no STUN).
func newHostOnlyPeerConnection() (*webrtc.PeerConnection, error) {
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(webrtc.Configuration{})
}

// BuildWebRTCHandler creates an offerer that signals over HTTP (POST offer SDP → answer SDP)
// and bridges DataChannel JSON messages to the same in/out chans as NDJSON handlers.
func BuildWebRTCHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if out == nil {
			return nil, fmt.Errorf("webrtc requires an output channel")
		}
		defer close(out)

		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		pc, err := newHostOnlyPeerConnection()
		if err != nil {
			return nil, fmt.Errorf("webrtc peer connection: %w", err)
		}
		defer func() { _ = pc.Close() }()

		dc, err := pc.CreateDataChannel("proxyma", nil)
		if err != nil {
			return nil, fmt.Errorf("webrtc data channel: %w", err)
		}

		opened := make(chan struct{})
		dc.OnOpen(func() { close(opened) })
		closed := make(chan struct{})
		dc.OnClose(func() { close(closed) })

		recvErr := make(chan error, 1)
		var errOnce sync.Once
		fail := func(err error) {
			if err != nil {
				errOnce.Do(func() { recvErr <- err })
			}
		}

		var received atomic.Int64
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			var chunk map[string]any
			if err := json.Unmarshal(msg.Data, &chunk); err != nil {
				fail(fmt.Errorf("webrtc decode chunk: %w", err))
				return
			}
			select {
			case <-ctx.Done():
			case out <- chunk:
				received.Add(1)
			}
		})
		dc.OnError(fail)

		if err := negotiateOfferer(ctx, pc, endpointURL, timeout); err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-recvErr:
			return nil, err
		case <-opened:
		}

		if in == nil {
			if payload == nil {
				payload = map[string]any{}
			}
			tmp := make(chan map[string]any, 1)
			tmp <- payload
			close(tmp)
			in = tmp
		}

		sent, err := pumpJSONToDataChannel(ctx, dc, in)
		if err != nil {
			return nil, err
		}
		if err := waitDataChannelDrain(ctx, &received, sent, recvErr, closed); err != nil {
			return nil, err
		}
		_ = dc.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-recvErr:
			return nil, err
		case <-closed:
			return nil, nil
		}
	}
}

func negotiateOfferer(ctx context.Context, pc *webrtc.PeerConnection, signalingURL string, timeout time.Duration) error {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("webrtc create offer: %w", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("webrtc set local description: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gatherDone:
	}

	clientTimeout := time.Duration(0)
	if timeout > 0 {
		clientTimeout = timeout
	}
	client := p2p.NewHTTPClient(nil, clientTimeout)
	resp, err := p2p.PostJSONAbsolute(ctx, client, signalingURL, pc.LocalDescription())
	if err != nil {
		return fmt.Errorf("webrtc signaling request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !utils.HTTPSuccess(resp.StatusCode) {
		bodyStr, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webrtc signaling returned status %d: %s", resp.StatusCode, string(bodyStr))
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return fmt.Errorf("webrtc decode answer: %w", err)
	}
	if err := pc.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("webrtc set remote description: %w", err)
	}
	return nil
}

func waitDataChannelDrain(ctx context.Context, received *atomic.Int64, sent int64, recvErr <-chan error, closed <-chan struct{}) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for received.Load() < sent {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErr:
			return err
		case <-closed:
			return nil
		case <-tick.C:
		}
	}
	return nil
}

func pumpJSONToDataChannel(ctx context.Context, dc *webrtc.DataChannel, in <-chan map[string]any) (int64, error) {
	var sent int64
	for {
		select {
		case <-ctx.Done():
			return sent, ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return sent, nil
			}
			b, err := json.Marshal(msg)
			if err != nil {
				return sent, fmt.Errorf("webrtc marshal chunk: %w", err)
			}
			if err := dc.SendText(string(b)); err != nil {
				return sent, fmt.Errorf("webrtc send chunk: %w", err)
			}
			sent++
		}
	}
}
