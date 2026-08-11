package p2p_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMTLSConnectionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	node := testutil.NewNodeTLS(t, "1")
	serverTLS, clientTLS := node.ServerTLS, node.ClientTLS
	handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("hyper secure connection")); err != nil {
			require.NoError(t, err)
		}
	})

	testSlog := protocol.NewLogger(testutil.TestLogWriter{T: t}, true).With("node", "Test17-mTLS")
	secureServer := httptest.NewUnstartedServer(handlerFunc)
	secureServer.TLS = serverTLS

	secureServer.Config.ErrorLog = slog.NewLogLogger(testSlog.Handler(), slog.LevelError)
	secureServer.StartTLS()
	defer secureServer.Close()

	t.Run("Client succesfully connects to the server", func(t *testing.T) {
		legitClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTLS,
			},
		}

		resp, err := legitClient.Get(secureServer.URL)
		require.NoError(t, err, "The client should be able to connect")
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("Reject certificates from an unknown CA", func(t *testing.T) {
		hackerDir := t.TempDir()
		err := p2p.InitCluster(hackerDir)
		require.NoError(t, err)
		err = p2p.IssueNodeCertificate(hackerDir, hackerDir, "hacker-node")
		require.NoError(t, err)
		caCertFile, _ := p2p.CACertPaths(hackerDir)
		nodeCertFile, nodeKeyFile := p2p.NodeCertPaths(hackerDir, "hacker-node")
		_, hackerClientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
		require.NoError(t, err)

		hackerClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: hackerClientTLS,
			},
		}

		_, err = hackerClient.Get(secureServer.URL)
		require.Error(t, err, "Should fail because the CA is not the same from what the cluster use")
		require.Contains(t, err.Error(), "bad certificate", "The server should reject unknown origin of certificates")
	})
}

func TestHTTPPeerClientFetchManifest(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathManifest, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"file1.txt":{"name":"file1.txt","hash":"abc123","version":1,"size":42}}`))
	})
	addr, client := newMockServer(t, mux)

	ctx := context.Background()
	manifest, err := client.FetchManifest(ctx, addr)
	require.NoError(t, err)
	require.Len(t, manifest, 1)
	require.Equal(t, "file1.txt", manifest["file1.txt"].Name)
	require.Equal(t, "abc123", manifest["file1.txt"].Hash)
	require.Equal(t, 1, manifest["file1.txt"].Version)
}

func TestHTTPPeerClientDownloadBlob(t *testing.T) {
	t.Parallel()
	expectedContent := "binary blob content here"

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathDownloadPrefix, func(w http.ResponseWriter, r *http.Request) {
		hash := r.URL.Path[len(protocol.PathDownloadPrefix):]
		if hash != "abc123" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(expectedContent))
	})
	addr, client := newMockServer(t, mux)

	ctx := context.Background()
	body, err := client.DownloadBlob(ctx, addr, "abc123")
	require.NoError(t, err)
	defer func() { _ = body.Close() }()

	content, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, expectedContent, string(content))
}

func TestHTTPPeerClientNotify(t *testing.T) {
	t.Parallel()
	var received protocol.PeerNotification
	notifyCalled := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathNotify, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		err := json.NewDecoder(r.Body).Decode(&received)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		notifyCalled <- struct{}{}
	})
	addr, client := newMockServer(t, mux)

	notification := protocol.PeerNotification{
		File:   protocol.IndexEntry{Name: "updated.txt", Hash: "hash999", Version: 3},
		Source: "https://some-peer:8080",
	}

	ctx := context.Background()
	err := client.Notify(ctx, addr, notification)
	require.NoError(t, err)

	select {
	case <-notifyCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify handler was never called")
	}

	require.Equal(t, "updated.txt", received.File.Name)
	require.Equal(t, "hash999", received.File.Hash)
	require.Equal(t, "https://some-peer:8080", received.Source)
}

func TestHTTPPeerClientNotifyServiceUpdate(t *testing.T) {
	t.Parallel()
	var received protocol.ServiceNotification
	notifyCalled := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathServicesNotify, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		err := json.NewDecoder(r.Body).Decode(&received)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		notifyCalled <- struct{}{}
	})
	addr, client := newMockServer(t, mux)

	notification := protocol.ServiceNotification{
		Action: "add",
		NodeID: "test-node",
		Schema: protocol.ServiceSchema{Name: "test-svc"},
	}

	ctx := context.Background()
	err := client.NotifyServiceUpdate(ctx, addr, notification)
	require.NoError(t, err)

	select {
	case <-notifyCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("NotifyServiceUpdate handler was never called")
	}

	require.Equal(t, "add", received.Action)
	require.Equal(t, "test-node", received.NodeID)
	require.Equal(t, "test-svc", received.Schema.Name)
}

func TestHTTPPeerClientAnnounce(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathPeersAnnounce, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req protocol.AddPeerRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		require.Equal(t, "newcomer", req.ID)

		w.Header().Set("Content-Type", "application/json")
		// Respond with the cluster topology including the newcomer
		_, _ = w.Write([]byte(`{"sponsor":{"addresses":["https://sponsor:8080"]},"newcomer":{"addresses":["https://newcomer:9090"]}}`))
	})
	addr, client := newMockServer(t, mux)

	peers, err := client.Announce(addr, protocol.AddPeerRequest{
		ID:      "newcomer",
		Address: protocol.AddressRecord{Addresses: []string{"https://newcomer:9090"}},
	})
	require.NoError(t, err)
	require.Len(t, peers, 2)
	require.Equal(t, "https://sponsor:8080", peers["sponsor"].Addresses[0])
	require.Equal(t, "https://newcomer:9090", peers["newcomer"].Addresses[0])
}

func TestHTTPPeerClientAddPeer(t *testing.T) {
	t.Parallel()
	addPeerCalled := make(chan struct{}, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.PathPeersAdd, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var req protocol.AddPeerRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		require.Equal(t, "new-node", req.ID)
		require.Equal(t, "https://new-node:8080", req.Address.Addresses[0])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"Peer successfully added"}`))
		addPeerCalled <- struct{}{}
	})
	addr, client := newMockServer(t, mux)

	payload := bytes.NewBuffer([]byte(`{"id":"new-node","address":{"addresses":["https://new-node:8080"]}}`))
	err := client.AddPeer(addr, payload)
	require.NoError(t, err)

	select {
	case <-addPeerCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("AddPeer handler was never called")
	}
}

func TestHTTPPeerClientSubmitAndCallback(t *testing.T) {
	t.Parallel()

	// This mock server handles both /services/submit and /services/callback
	mux := http.NewServeMux()

	mux.HandleFunc(protocol.PathServicesSubmit, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var taskReq protocol.TaskRequest
		err := json.NewDecoder(r.Body).Decode(&taskReq)
		require.NoError(t, err)
		require.Equal(t, "task-001", taskReq.TaskID)
		require.Equal(t, "ocr", taskReq.Service)

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","job_id":"task-001"}`))
	})

	callbackReceived := make(chan protocol.ServiceTaskResponse, 1)
	mux.HandleFunc(protocol.PathServicesCallback, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		var resp protocol.ServiceTaskResponse
		err := json.NewDecoder(r.Body).Decode(&resp)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		callbackReceived <- resp
	})

	addr, client := newMockServer(t, mux)

	// Test SubmitTask
	ctx := context.Background()
	taskReq := protocol.TaskRequest{
		TaskID:  "task-001",
		Service: "ocr",
		Payload: map[string]any{"image": "hash-abc"},
		ReplyTo: addr + protocol.PathServicesCallback,
	}
	err := client.SubmitTask(ctx, addr, taskReq)
	require.NoError(t, err)

	// Test SendTaskResponse (callback)
	taskResp := protocol.ServiceTaskResponse{
		TaskID:  "task-001",
		Service: "ocr",
		Status:  "completed",
		Outputs: map[string]any{"text": "Hello world"},
	}
	err = client.SendTaskResponse(ctx, addr+protocol.PathServicesCallback, taskResp)
	require.NoError(t, err)

	select {
	case received := <-callbackReceived:
		require.Equal(t, "task-001", received.TaskID)
		require.Equal(t, "completed", received.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("Callback handler was never called")
	}
}
