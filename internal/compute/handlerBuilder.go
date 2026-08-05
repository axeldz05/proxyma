package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"proxyma/internal/p2p"
	"proxyma/internal/utils"
	"time"
)

// doJSONPost POSTs JSON to endpointURL and decodes a JSON object response (L2).
func doJSONPost(ctx context.Context, client *http.Client, endpointURL string, payload map[string]any) (map[string]any, error) {
	resp, err := p2p.PostJSONAbsolute(ctx, client, endpointURL, payload)
	if err != nil {
		return nil, fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !utils.HTTPSuccess(resp.StatusCode) {
		bodyStr, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("remote server returned status %d: %s", resp.StatusCode, string(bodyStr))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode remote response: %w", err)
	}
	return result, nil
}

// BuildUnaryHandler wraps a simple unary function into a ServiceHandler.
func BuildUnaryHandler(fn func(ctx context.Context, payload map[string]any) (map[string]any, error)) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return fn(ctx, payload)
	}
}

// Handler generator for external scripts
func BuildScriptHandler(executablePath string) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if in != nil && out != nil {
			defer close(out)
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", executablePath)
			stdinPipe, err := cmd.StdinPipe()
			if err != nil {
				return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
			}
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				_ = stdinPipe.Close()
				return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
			}

			if err := cmd.Start(); err != nil {
				return nil, fmt.Errorf("failed to start script: %w", err)
			}
			defer func() { _ = cmd.Wait() }()

			go func() {
				defer func() { _ = stdinPipe.Close() }()
				_ = utils.PumpJSONEncode(ctx, stdinPipe, in)
			}()

			_ = utils.PumpJSONDecode(ctx, stdoutPipe, out)
			return nil, nil
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}

		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", executablePath)
		cmd.Stdin = bytes.NewReader(payloadBytes)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			var result map[string]any
			if json.Unmarshal(stdout.Bytes(), &result) == nil {
				if errMsg, ok := result["error"].(string); ok && errMsg != "" {
					return nil, fmt.Errorf("%s", errMsg)
				}
			}
			return nil, fmt.Errorf("external script failed: %v, stdout: %s, stderr: %s", err, stdout.String(), stderr.String())
		}

		var result map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			return nil, fmt.Errorf("external script returned invalid JSON: %w (output: %s)", err, stdout.String())
		}

		return result, nil
	}
}

// Handler generator for gRPC webhooks
func BuildGRPCHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	client := p2p.NewHTTPClient(nil, timeout)

	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return doJSONPost(ctx, client, endpointURL, payload)
	}
}

// notImplementedHandler generates a stub for unsupported connections.
func notImplementedHandler(name string) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("%s not yet implemented", name)
	}
}

// BuildGRPCBidiHandler creates a ServiceHandler for Bidirectional Streaming.
func BuildGRPCBidiHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if in != nil && out != nil {
			defer close(out)

			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			pr, pw := io.Pipe()

			go func() {
				defer func() { _ = pw.Close() }()
				if err := utils.PumpJSONEncode(ctx, pw, in); err != nil {
					_ = pw.CloseWithError(err)
				}
			}()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, pr)
			if err != nil {
				_ = pr.Close()
				return nil, fmt.Errorf("failed to create bidi stream request: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-ndjson")

			// Streaming: rely on ctx for deadline; avoid client-level timeout that
			// would cut long-lived NDJSON pipes when handler timeout is 0.
			clientTimeout := time.Duration(0)
			if timeout > 0 {
				clientTimeout = timeout
			}
			client := p2p.NewHTTPClient(nil, clientTimeout)
			resp, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("bidi stream request failed: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if !utils.HTTPSuccess(resp.StatusCode) {
				bodyStr, _ := io.ReadAll(resp.Body)
				return nil, fmt.Errorf("remote bidi stream server returned status %d: %s", resp.StatusCode, string(bodyStr))
			}

			if err := utils.PumpJSONDecode(ctx, resp.Body, out); err != nil {
				return nil, fmt.Errorf("failed to decode bidi response chunk: %w", err)
			}
			return nil, nil
		}

		sin := make(chan map[string]any, 1)
		sout := make(chan map[string]any, 1)
		errChan := make(chan error, 1)

		bidiFunc := BuildGRPCBidiHandler(endpointURL, timeout)
		go func() {
			errChan <- bidiFunc.ExecuteStream(ctx, sin, sout)
		}()

		sin <- payload
		close(sin)

		var response map[string]any
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errChan:
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("bidi stream closed without returning response")
		case res, ok := <-sout:
			if ok {
				response = res
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-errChan:
			if err != nil {
				return nil, err
			}
			if response != nil {
				return response, nil
			}
			return nil, fmt.Errorf("bidi stream closed without returning response")
		}
	}
}

// BuildGRPCServerStreamHandler creates a handler for Server-Streaming.
func BuildGRPCServerStreamHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return notImplementedHandler("BuildGRPCServerStreamHandler")
}

// BuildWebRTCHandler creates a handler for WebRTC connections.
func BuildWebRTCHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return notImplementedHandler("BuildWebRTCHandler")
}
