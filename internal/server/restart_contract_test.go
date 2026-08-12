package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
)

func TestPublicHTTPContractSurvivesServerRestart(t *testing.T) {
	storagePath := t.TempDir()
	const nodeID = "http-restart-contract"
	if err := p2p.SetupNewNode(
		storagePath,
		nodeID,
		protocol.HTTPSAddr("127.0.0.1", "0"),
	); err != nil {
		t.Fatalf("set up restart node: %v", err)
	}

	first, firstServe, firstClient, firstAddress := startRestartContractServer(t, storagePath)
	firstStopped := false
	t.Cleanup(func() {
		if !firstStopped {
			stopRestartContractServer(t, first, firstServe)
		}
	})

	const fileName = "restart-contract.txt"
	content := []byte("public VFS state survives restart")
	uploadRestartContractFile(t, firstClient, firstAddress, fileName, content)
	beforeRestart := requestRestartContractManifest(t, firstClient, firstAddress)
	beforeEntry, ok := beforeRestart[fileName]
	if !ok || beforeEntry.Deleted || beforeEntry.Hash == "" || beforeEntry.Size != int64(len(content)) {
		t.Fatalf("manifest before restart = %#v, want live %s metadata", beforeRestart, fileName)
	}

	firstStopped = true
	stopRestartContractServer(t, first, firstServe)

	second, secondServe, secondClient, secondAddress := startRestartContractServer(t, storagePath)
	t.Cleanup(func() { stopRestartContractServer(t, second, secondServe) })
	afterRestart := requestRestartContractManifest(t, secondClient, secondAddress)
	afterEntry, ok := afterRestart[fileName]
	if !ok || afterEntry != beforeEntry {
		t.Fatalf("manifest after restart = %#v, want persisted metadata %#v", afterRestart, beforeEntry)
	}
	downloaded := downloadRestartContractBlob(t, secondClient, secondAddress, afterEntry.Hash)
	if !bytes.Equal(downloaded, content) {
		t.Fatalf("download after restart = %q, want %q", downloaded, content)
	}
}

func startRestartContractServer(
	t *testing.T,
	storagePath string,
) (*server.Server, <-chan error, *http.Client, string) {
	t.Helper()
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		t.Fatalf("load restart config: %v", err)
	}
	cfg.Logger = protocol.NewLogger(&protocol.LogWriter{Stdout: io.Discard}, false)

	certPath, keyPath := p2p.NodeCertPaths(filepath.Join(storagePath, "certs"), cfg.ID)
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(cfg.CAPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("load restart TLS: %v", err)
	}
	app, err := server.New(cfg, nil)
	if err != nil {
		t.Fatalf("create restart server: %v", err)
	}
	app.SetTLSConfigs(serverTLS, clientTLS)

	serveResult := make(chan error, 1)
	go func() { serveResult <- app.ListenAndServe(serverTLS) }()
	select {
	case <-app.Ready():
	case err := <-serveResult:
		t.Fatalf("start restart server: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("restart server did not become ready")
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   clientTLS,
			DisableKeepAlives: true,
		},
		Timeout: 3 * time.Second,
	}
	return app, serveResult, client, app.Config.Address
}

func stopRestartContractServer(
	t *testing.T,
	app *server.Server,
	serveResult <-chan error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown restart server: %v", err)
	}
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("server exit = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP listener did not exit after shutdown")
	}
}

func uploadRestartContractFile(
	t *testing.T,
	client *http.Client,
	address string,
	name string,
	content []byte,
) {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	filePart, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filePart.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, address+protocol.PathUpload, &requestBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("upload through public mTLS API: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("public upload status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
}

func requestRestartContractManifest(
	t *testing.T,
	client *http.Client,
	address string,
) map[string]protocol.IndexEntry {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, address+protocol.PathManifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("list manifest through public mTLS API: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public manifest status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var manifest map[string]protocol.IndexEntry
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode public manifest: %v", err)
	}
	return manifest
}

func downloadRestartContractBlob(
	t *testing.T,
	client *http.Client,
	address string,
	hash string,
) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, address+protocol.PathDownloadPrefix+hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("download through public mTLS API: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public download status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read public download: %v", err)
	}
	return content
}
