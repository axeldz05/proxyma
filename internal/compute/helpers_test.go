package compute_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)
func setupMockExecutable(t *testing.T) string {
	t.Helper()

	tempDir := t.TempDir()
	sourcePath := filepath.Join(tempDir, "mock_script.go")
	binaryPath := filepath.Join(tempDir, "mock_bin")

	// The mock script reads JSON from stdin.
	// It uses the "test_scenario" field injected by the test to determine its behavior.
	sourceCode := `
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(1)
	}

	var payload map[string]any
	if err := json.Unmarshal(inputBytes, &payload); err != nil {
		os.Exit(1)
	}

	scenario, _ := payload["test_scenario"].(string)

	switch scenario {
	case "success":
		// Echo the payload back with some modifications
		payload["status"] = "ok"
		payload["processed_by"] = "mock_binary"
		out, _ := json.Marshal(payload)
		fmt.Print(string(out))
		
	case "fail_execution":
		// Simulate a script crash or expected error
		fmt.Fprint(os.Stderr, "simulated script crash due to invalid state")
		os.Exit(1)
		
	case "invalid_json":
		// Simulate a script returning malformed data
		fmt.Print("{ this is not valid json ]")
		
	default:
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(sourcePath, []byte(sourceCode), 0644); err != nil {
		t.Fatalf("Failed to write mock source code: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to compile mock binary: %v", err)
	}

	return binaryPath
}

// setupMockGRPCWebhookServer creates an HTTP server that simulates a gRPC/Webhook endpoint.
// It parses incoming JSON requests and acts based on the "test_scenario" parameter.
func setupMockGRPCWebhookServer(t *testing.T) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate gRPC content type check or webhook validation
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		scenario, _ := payload["test_scenario"].(string)

		switch scenario {
		case "success":
			w.Header().Set("Content-Type", "application/json") // Or application/grpc+json
			w.WriteHeader(http.StatusOK)
			payload["status"] = "ok"
			payload["processed_by"] = "grpc_webhook_mock"
			_ = json.NewEncoder(w).Encode(payload)

		case "fail_server_error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "internal gRPC server error"}`))

		case "fail_timeout":
			// Simulate a hanging RPC call that exceeds the context timeout
			time.Sleep(3 * time.Second)
			w.WriteHeader(http.StatusOK)

		case "invalid_response":
			// Simulate a corrupted response from the remote node
			w.WriteHeader(http.StatusOK)
			_,_ = w.Write([]byte(`{ "broken_json": true, `))

		default:
			http.Error(w, "unknown scenario", http.StatusBadRequest)
		}
	})

	return httptest.NewServer(handler)
}

