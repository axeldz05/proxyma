package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type TestServer struct {
	*server.Server
	httpTestSrv *httptest.Server
}

func (ts *TestServer) Client() *http.Client {
	return ts.httpTestSrv.Client()
}

func NewServer(t *testing.T, cfg protocol.NodeConfig, mockClient p2p.PeerClient) *TestServer {
	caPath := filepath.Dir(cfg.StoragePath)
	err := p2p.InitCluster(caPath)
	require.NoError(t, err)
	err = p2p.IssueNodeCertificate(caPath, cfg.StoragePath, cfg.ID)
	require.NoError(t, err)
	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(cfg.StoragePath, cfg.ID+".crt")
	nodeKeyFile := filepath.Join(cfg.StoragePath, cfg.ID+".key")
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)

	customTransport := &http.Transport{
		TLSClientConfig:   clientTLS,
		DisableKeepAlives: true, 
	}

	var finalClient p2p.PeerClient
	if mockClient != nil {
		finalClient = mockClient
	} else {
		httpClient := &http.Client{
			Transport: customTransport,
		}
		finalClient = p2p.NewHTTPPeerClient(httpClient)
	}

	app := server.New(cfg, finalClient)
	ts := httptest.NewUnstartedServer(app.MountHandlers())
	ts.TLS = serverTLS
	ts.StartTLS()

	ts.Client().Transport = &http.Transport{
		TLSClientConfig:   clientTLS,
		DisableKeepAlives: true,
	}
	app.SetAddress(ts.URL)

	t.Cleanup(func() {
		ts.CloseClientConnections()
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        err := app.Shutdown(ctx)
        require.NoError(t, err, "Node shutdown should not return an error")
		ts.Close()
    })

	return &TestServer{
		Server:      app,
		httpTestSrv: ts,
	}
}

func UploadFileSimulated(t *testing.T, sv *TestServer, fileName, content string) string {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = io.WriteString(fileWriter, content)
	require.NoError(t, err)
	err = writer.Close() 
    require.NoError(t, err, "Failed to close multipart writer")

	reqUp, err := http.NewRequest("POST", sv.Config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())

	respUp, err := sv.Client().Do(reqUp)
	require.NoError(t, err)
	defer func(){ _ = respUp.Body.Close() }()

	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return testutil.CalculateHash(t, content)
}

func DeleteFileSimulated(t *testing.T, sv *TestServer, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.Config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)

	respDel, err := sv.Client().Do(reqDel)
	require.NoError(t, err)
	defer func(){ _ = respDel.Body.Close() }()

	require.Equal(t, http.StatusOK, respDel.StatusCode, "Delete should have return 200 OK")
}

func GetPeersSimulated(t *testing.T, sv *TestServer) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()
	sv.GetPeers(w, req)
	resp := w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	return buf.String()
}

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, updatedServer *TestServer) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.Config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	resp, err := updatedServer.Client().Do(req)
	require.NoError(t, err)
	defer func(){ _ = resp.Body.Close() }()
	buf := new(strings.Builder)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy fileContent from %s", resp.Body)
	}
	uploadedContent := buf.String()

	if uploadedContent != fileContent {
		t.Errorf("Expected content %s, got %s", fileContent, string(uploadedContent))
	}
}

func TestAServerCanConnectToAnother(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	sv2 := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)

	gotPeersOfSv2 := strings.TrimSpace(GetPeersSimulated(t, sv2))
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.Config.Address)
	gotPeersOfSv1 := strings.TrimSpace(GetPeersSimulated(t, sv1))
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.Config.Address)

	require.Equal(t, expectedPeersOfSv2, gotPeersOfSv2)
	require.Equal(t, expectedPeersOfSv1, gotPeersOfSv1)
	require.NoError(t, sv1.ExecuteSync([]string{"1"}))
	require.NoError(t, sv2.ExecuteSync([]string{"2"}))
}

func TestP2PNetworkEventualConsistency(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*TestServer, clusterSize)
	for i := range clusterSize {
		serverName := fmt.Sprintf("%d", i)
		servers[i] = NewServer(t, testutil.DefaultConfig(t, serverName), nil)
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.Config.ID, peer.Config.Address)
			}
		}
	}
	fileName := "test05.txt"
	for _, srv := range servers {
		srv.Storage.SetSubscription(fileName, true)
	}
	fileContent := "everyone wants me"
	expectedHash := UploadFileSimulated(t, servers[0], fileName, fileContent)
	require.Eventually(t, func() bool {
		for _, srv := range servers {
			meta, exists := srv.Storage.GetFileMeta(fileName)
			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func TestAllServersSyncsToLastUpdated(t *testing.T) {
	t.Parallel()
	updatedServer := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	noUpdatedServer := NewServer(t, testutil.DefaultConfig(t, "2"), nil)
	noUpdatedServer2 := NewServer(t, testutil.DefaultConfig(t, "3"), nil)

	updatedServer.AddPeer("2", noUpdatedServer.Config.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.Config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Config.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.Config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Config.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.Config.Address)
	fileName := "test03.txt"
	noUpdatedServer.Storage.SetSubscription(fileName, true)
	noUpdatedServer2.Storage.SetSubscription(fileName, true)

	fileContent := "Hello!!!"
	expectedHash := UploadFileSimulated(t, updatedServer, fileName, fileContent)

	_, exists := updatedServer.Storage.GetFileMeta(fileName)
	if !exists {
		t.Errorf("Blob hash '%s' was not registered in the metadata", expectedHash)
	}

	assertRemoteHashToBeTheSameAs(t, expectedHash, fileContent, updatedServer)
	require.Eventually(t, func() bool {
		_, exists := noUpdatedServer.Storage.GetFileMeta(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func TestUploadEndpointReturnsAndRegistersHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"), nil)

	fileName := "test04.txt"
	fileContent := "testing"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	fileMeta, exists := sv.Storage.GetFileMeta(fileName)

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func TestDownloadEndpointUsesHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	fileName := "test06.txt"
	fileContent := "Hello!!"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	downloadURL := fmt.Sprintf("%s/download/%s", sv.Config.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	respDL, err := sv.Client().Do(reqDL)
	require.NoError(t, err)
	defer func(){ _ = respDL.Body.Close() }()

	require.Equal(t, http.StatusOK, respDL.StatusCode, "Server should answer with OK 200 status when requesting Hash")
	buf := new(strings.Builder)
	_, err = io.Copy(buf, respDL.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, buf.String(), "Downloaded content should be the same as the uploaded content")
}

func TestManifestEndpointReturnsCurrentState(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"), nil)

	fakeHash := "hash-simulado-999"
	fakeFile := protocol.IndexEntry{
		Name: "dataset_v2.csv",
		Size: 1024,
		Hash: fakeHash,
	}

	sv.Storage.Upsert(fakeFile)

	req, err := http.NewRequest("GET", sv.Config.Address+"/manifest", nil)
	require.NoError(t, err)

	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func(){ _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode, "The endpoint /manifest must answer with status code: 200 OK")

	var manifest map[string]protocol.IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err, "The manifest must be a valid JSON in format: map[string]FileInfo")

	require.Contains(t, manifest[fakeFile.Name].Hash, fakeHash, "The manifest must contain the hash of the injected file")
	require.Equal(t, fakeFile.Name, manifest[fakeFile.Name].Name, "The filename must be the same as in the manifest")
}

func TestTombstonePropagatesToPeers(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	sv2 := NewServer(t, testutil.DefaultConfig(t, "2"), nil)

	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)

	fileName := "test14.txt"
	sv1.Storage.SetSubscription(fileName, true)
	sv2.Storage.SetSubscription(fileName, true)

	fileContent := "hello from test14!!"
	UploadFileSimulated(t, sv1, fileName, fileContent)

	require.Eventually(t, func() bool {
		_, exists := sv2.Storage.GetFileMeta(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond)

	DeleteFileSimulated(t, sv1, fileName)

	require.Eventually(t, func() bool {
		meta, _ := sv2.Storage.GetFileMeta(fileName)
		return meta.Deleted && meta.Version == 2
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have processed the Tombstone")
}

func TestANodeReceivesSatisfactoryAnswerFromServiceRequest(t *testing.T) {
	t.Parallel()
	svWithService := NewServer(t, testutil.DefaultConfig(t, "1"), nil)
	svDemandingService := NewServer(t, testutil.DefaultConfig(t, "2"), nil)

	savedParameters := map[string]protocol.ServiceParameter{
		"image":    {Type: "string", Required: true},
		"language": {Type: "string", Required: false},
		"output":   {Type: "string", Required: false},
	}
	schema1 := protocol.ServiceSchema{
		Name:        "ocr",
		Description: "Standard Optical Character Recognition",
		Parameters:  savedParameters,
	}
	var mockHandler compute.ServiceHandler = func(context.Context, map[string]any) (map[string]any, error) {
        return map[string]any{}, nil
    }
	err := svWithService.Compute.RegisterNewService(schema1, mockHandler)
	require.NoError(t, err)

	svDemandingService.AddPeer(svWithService.Config.ID, svWithService.Config.Address)
	svWithService.AddPeer(svDemandingService.Config.ID, svDemandingService.Config.Address)

	query := protocol.DiscoveryQuery{
		Service:          "ocr",
		RequiredParams:   []string{"language"},
		SortStrategy:     protocol.StrategyFastest,
		PayloadSizeBytes: 1024 * 1024 * 5,
	}

	targetPeerAddr, serviceSchema, err := svDemandingService.RequestServiceToCluster(query)
	require.NoError(t, err)
	require.Equal(t, svWithService.Config.Address, targetPeerAddr, "Debería haber elegido al nodo 1")
	require.Equal(t, "Standard Optical Character Recognition", serviceSchema.Description)

	filledInputs := map[string]any{
		"image":    "fake-hash-12345",
		"language": "spa",
	}

	taskID := "job-999"
	reqPayload := protocol.TaskRequest{
		TaskID:  taskID,
		Service: "ocr",
		Payload: filledInputs,
		ReplyTo: svDemandingService.Config.Address + "/services/callback",
	}

	err = svDemandingService.DispatchTask(targetPeerAddr, reqPayload)
	require.NoError(t, err, "The node worker should have accepted the task")

	require.Eventually(t, func() bool {
		taskResult, exists := svDemandingService.Compute.GetTaskStatus(taskID)
		return exists && taskResult.Status == "completed"
	}, 2*time.Second, 100*time.Millisecond, "The completion Webhook never arrived")
}

func TestUnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"), nil)

	clientWithoutCert := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := clientWithoutCert.Get(sv.Config.Address + "/peers")
	if resp != nil {
		defer func(){ _ = resp.Body.Close() }()
	}
	require.Error(t, err, "Should fail at TLS handshake due to missing client certs")
}

func TestServerWorkerPoolLimitsConcurrency(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-server-1")
	cfg.Workers = 2

	contentByHash := make(map[string]string)
	manifest := make(map[string]protocol.IndexEntry)

	for i := range 5 {
		content := fmt.Sprintf("content %d", i)
		hash := testutil.CalculateHash(t, content)
		fileName := fmt.Sprintf("file_%d.txt", i)
		contentByHash[hash] = content
		manifest[fileName] = protocol.IndexEntry{
			Name: fileName, Hash: hash, Version: 1,
		}
	}

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return manifest, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			time.Sleep(1 * time.Second)
			content, ok := contentByHash[hash]
			if !ok {
				return nil, fmt.Errorf("hash not found in mock")
			}
			return io.NopCloser(bytes.NewReader([]byte(content))), nil
		},
	}

	srv := NewServer(t, cfg, mockClient) 
	for i := range 5 {
		srv.Storage.SetSubscription(fmt.Sprintf("file_%d.txt", i), true)
	}
	srv.AddPeer("peer1", "https://fake:8080")
	start := time.Now()
	err := srv.ExecuteSync([]string{"peer1"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		snapshot := srv.Storage.GetVFSSnapshot()
		if len(snapshot) < 5 {
			return false
		}
		for _, v := range snapshot {
			hasBlob, _ := srv.Storage.HasPhysicalBlob(v.Hash)
			if !hasBlob {
				return false
			}
		}
		return true
	}, 6*time.Second, 100*time.Millisecond)

	duration := time.Since(start)
	
	// 5 files at 1 seg per file, with 2 workers, should take ~3 seconds.
	// if it takes < 2s, it's downloading everything at once.
	// if it takes >= 5s, the concurrency is failing. 
	require.GreaterOrEqual(t, duration, 2*time.Second, "Too fast. Worker pool isn't limiting concurrency.")
	require.Less(t, duration, 4*time.Second, "Too slow. System is working sequentially.")
}

func TestServerExecuteSyncRespectsTimeouts(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-server-2")

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return map[string]protocol.IndexEntry{}, nil
			}
		},
	}

	srv := NewServer(t, cfg, mockClient) 
	srv.AddPeer("slow-peer", "https://fake-address:8080")
	start := time.Now()

	err := srv.ExecuteSync([]string{"slow-peer"})
	require.NoError(t, err)

	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 2*time.Second, "Exited too early, didn't wait for timeout")
	require.Less(t, duration, 3*time.Second, "Hung too long, failed to respect context timeout")
}
