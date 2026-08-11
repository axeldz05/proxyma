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

// withHandlerTimeout bounds a handler run when a positive timeout is configured (L2).
// A zero timeout keeps the caller's context so long-lived NDJSON pipes are not cut.
func withHandlerTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// streamHTTPClient builds the client used by streaming handlers (L2). Deadlines
// come from the context; a client-level timeout only applies when one is set.
func streamHTTPClient(timeout time.Duration) *http.Client {
	clientTimeout := time.Duration(0)
	if timeout > 0 {
		clientTimeout = timeout
	}
	return p2p.NewHTTPClient(nil, clientTimeout)
}

// doJSONPost POSTs JSON to endpointURL and decodes a JSON object response (L2).
func doJSONPost(ctx context.Context, client *http.Client, endpointURL string, payload map[string]any) (map[string]any, error) {
	resp, err := p2p.PostJSONAbsolute(ctx, client, endpointURL, payload)
	if err != nil {
		return nil, fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := utils.HTTPErrorFromResponse(resp, "remote server"); err != nil {
		return nil, err
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
		if in != nil || out != nil {
			if out != nil {
				close(out)
			}
			return nil, fmt.Errorf("unary service does not support streaming")
		}
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

// BuildHTTPHandler creates a ServiceHandler for unary JSON HTTP webhooks.
func BuildHTTPHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	client := p2p.NewHTTPClient(nil, timeout)

	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if in != nil || out != nil {
			if out != nil {
				close(out)
			}
			return nil, fmt.Errorf("unary grpc service does not support streaming")
		}
		return doJSONPost(ctx, client, endpointURL, payload)
	}
}

// BuildHTTPBidiHandler creates a ServiceHandler for HTTP NDJSON bidirectional streaming.
func BuildHTTPBidiHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if in != nil && out != nil {
			defer close(out)

			ctx, cancel := withHandlerTimeout(ctx, timeout)
			defer cancel()

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

			resp, err := streamHTTPClient(timeout).Do(req)
			if err != nil {
				return nil, fmt.Errorf("bidi stream request failed: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if err := utils.HTTPErrorFromResponse(resp, "remote bidi stream server"); err != nil {
				return nil, err
			}

			if err := utils.PumpJSONDecode(ctx, resp.Body, out); err != nil {
				return nil, fmt.Errorf("failed to decode bidi response chunk: %w", err)
			}
			return nil, nil
		}

		sin := make(chan map[string]any, 1)
		sout := make(chan map[string]any, 1)
		errChan := make(chan error, 1)

		bidiFunc := BuildHTTPBidiHandler(endpointURL, timeout)
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

// BuildHTTPServerStreamHandler creates a handler for HTTP NDJSON server-streaming.
// POSTs JSON payload (or {} if empty) and pumps the NDJSON response body into out.
func BuildHTTPServerStreamHandler(endpointURL string, timeout time.Duration) ServiceHandler {
	return func(ctx context.Context, in <-chan map[string]any, out chan<- map[string]any, payload map[string]any) (map[string]any, error) {
		if out == nil {
			return nil, fmt.Errorf("server stream requires an output channel")
		}
		defer close(out)

		ctx, cancel := withHandlerTimeout(ctx, timeout)
		defer cancel()

		bodyPayload := payload
		if bodyPayload == nil {
			bodyPayload = map[string]any{}
		}
		if in != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case msg, ok := <-in:
				if ok && msg != nil {
					bodyPayload = msg
				}
			default:
			}
		}

		bodyBytes, err := json.Marshal(bodyPayload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal server-stream payload: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create server-stream request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/x-ndjson")

		resp, err := streamHTTPClient(timeout).Do(req)
		if err != nil {
			return nil, fmt.Errorf("server-stream request failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if err := utils.HTTPErrorFromResponse(resp, "remote server-stream"); err != nil {
			return nil, err
		}

		if err := utils.PumpJSONDecode(ctx, resp.Body, out); err != nil {
			return nil, fmt.Errorf("failed to decode server-stream chunk: %w", err)
		}
		return nil, nil
	}
}
