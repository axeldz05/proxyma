package compute
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// Handler generator for external scripts
func BuildScriptHandler(executablePath string) ServiceHandler {
	return func(ctx context.Context, payload map[string]any) (map[string]any, error) {
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
			return nil, fmt.Errorf("external script failed: %v, stderr: %s", err, stderr.String())
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

	return func(ctx context.Context, payload map[string]any) (map[string]any, error) {
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

