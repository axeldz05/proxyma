package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

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
				encoder := json.NewEncoder(stdinPipe)
				defer func() { _ = stdinPipe.Close() }()
				for {
					select {
					case <-ctx.Done():
						return
					case item, ok := <-in:
						if !ok {
							return
						}
						_ = encoder.Encode(item)
					}
				}
			}()

			decoder := json.NewDecoder(stdoutPipe)
			for {
				var result map[string]any
				if err := decoder.Decode(&result); err != nil {
					if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
						return nil, nil
					}
					if ctx.Err() != nil {
						return nil, nil
					}
					break
				}
				select {
				case <-ctx.Done():
					return nil, nil
				case out <- result:
				}
			}
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
	client := &http.Client{
		Timeout: timeout,
	}

	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal webhook payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(payloadBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json") // Or application/grpc+json

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("webhook request failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyStr, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("remote server returned status %d: %s", resp.StatusCode, string(bodyStr))
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("failed to decode remote response: %w", err)
		}

		return result, nil
	}
}

// notImplementedHandler generates a stub for unsupported connections.
func notImplementedHandler(name string) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("%s not yet implemented", name)
	}
}

// BuildGRPCBidiStreamHandler creates a ServiceHandler for Bidirectional Streaming.
func BuildGRPCBidiStreamHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return BuildGRPCBidiHandler(endpointURL, timeout)
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
				encoder := json.NewEncoder(pw)
				defer func() { _ = pw.Close() }()

				for {
					select {
					case <-ctx.Done():
						_ = pw.CloseWithError(ctx.Err())
						return
					case item, ok := <-in:
						if !ok {
							return
						}
						if err := encoder.Encode(item); err != nil {
							_ = pw.CloseWithError(err)
							return
						}
					}
				}
			}()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, pr)
			if err != nil {
				_ = pr.Close()
				return nil, fmt.Errorf("failed to create bidi stream request: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-ndjson")

			client := &http.Client{}
			resp, err := client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("bidi stream request failed: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				bodyStr, _ := io.ReadAll(resp.Body)
				return nil, fmt.Errorf("remote bidi stream server returned status %d: %s", resp.StatusCode, string(bodyStr))
			}

			decoder := json.NewDecoder(resp.Body)
			for {
				var result map[string]any
				if err := decoder.Decode(&result); err != nil {
					if errors.Is(err, io.EOF) {
						return nil, nil
					}
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					return nil, fmt.Errorf("failed to decode bidi response chunk: %w", err)
				}

				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case out <- result:
				}
			}
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
