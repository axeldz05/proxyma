package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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

func TestPeerAdditionAndConnectivity(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, testutil.DefaultConfig(t, "sv1"), nil)
	sv2 := NewServer(t, testutil.DefaultConfig(t, "sv2"), nil)

	// Add peer via HTTP endpoint
	addReq := protocol.AddPeerRequest{ID: sv2.Config.ID, Address: protocol.AddressRecord{Addresses: []string{sv2.Config.Address}}}
	body, _ := json.Marshal(addReq)
	req, err := http.NewRequest("POST", sv1.Config.Address+"/peers/add", bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sv1.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Add peer programmatically on the other side
	sv2.AddPeer(sv1.Config.ID, protocol.AddressRecord{Addresses: []string{sv1.Config.Address}})

	// Both peers should now know each other
	gotPeersSv1 := strings.TrimSpace(GetPeersSimulated(t, sv1))
	expectedSv1 := fmt.Sprintf(`{"%s":{"addresses":["%s"],"sequence":0,"is_sponsor":false}}`, sv2.Config.ID, sv2.Config.Address)
	require.Equal(t, expectedSv1, gotPeersSv1)

	gotPeersSv2 := strings.TrimSpace(GetPeersSimulated(t, sv2))
	expectedSv2 := fmt.Sprintf(`{"%s":{"addresses":["%s"],"sequence":0,"is_sponsor":false}}`, sv1.Config.ID, sv1.Config.Address)
	require.Equal(t, expectedSv2, gotPeersSv2)

	require.NoError(t, sv1.ExecuteSync())
	require.NoError(t, sv2.ExecuteSync())
}

func TestFilePropagationAcrossCluster(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*TestServer, clusterSize)
	for i := range clusterSize {
		servers[i] = NewServer(t, testutil.DefaultConfig(t, fmt.Sprintf("node-%d", i)), nil)
	}

	// Full mesh connection
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.Config.ID, protocol.AddressRecord{Addresses: []string{peer.Config.Address}})
			}
		}
	}

	fileName := "shared.txt"
	fileContent := "content to propagate"
	for _, srv := range servers {
		srv.Storage.SetSubscription(fileName, true)
	}

	expectedHash := UploadFileSimulated(t, servers[0], fileName, fileContent)

	// All nodes must eventually have the correct metadata and physical blob
	for _, srv := range servers {
		require.Eventually(t, func() bool {
			meta, exists := srv.Storage.GetFileMeta(fileName)
			if !exists || meta.Hash != expectedHash {
				return false
			}
			hasBlob, _ := srv.Storage.HasPhysicalBlob(expectedHash)
			return hasBlob
		}, 5*time.Second, 100*time.Millisecond, "Node %s did not sync file", srv.Config.ID)
	}
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
	defer func() { _ = respDL.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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

	sv1.AddPeer("2", protocol.AddressRecord{Addresses: []string{sv2.Config.Address}})
	sv2.AddPeer("1", protocol.AddressRecord{Addresses: []string{sv1.Config.Address}})

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
		"image":    {Type: "file", Required: true},
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

	svDemandingService.AddPeer(svWithService.Config.ID, protocol.AddressRecord{Addresses: []string{svWithService.Config.Address}})
	svWithService.AddPeer(svDemandingService.Config.ID, protocol.AddressRecord{Addresses: []string{svDemandingService.Config.Address}})

	query := protocol.DiscoveryQuery{
		Service:          "ocr",
		RequiredParams:   []string{"language"},
		SortStrategy:     protocol.StrategyFastest,
		PayloadSizeBytes: 1024 * 1024 * 5,
	}

	_, targetPeerAddr, serviceSchema, err := svDemandingService.RequestServiceToCluster(query)
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
		taskResult, exists := svWithService.Compute.GetTaskResponse(taskID)
		return exists && taskResult.Status == "completed"
	}, 2*time.Second, 100*time.Millisecond, "The completion Webhook never arrived")
}

func TestFileParameterTypePropagatesThroughDiscovery(t *testing.T) {
	t.Parallel()
	provider := NewServer(t, testutil.DefaultConfig(t, "file-provider"), nil)
	consumer := NewServer(t, testutil.DefaultConfig(t, "file-consumer"), nil)

	schema := protocol.ServiceSchema{
		Name:        "pdf-converter",
		Description: "Converts documents to PDF format",
		Parameters: map[string]protocol.ServiceParameter{
			"input":   {Type: "file", Required: true},
			"quality": {Type: "int", Required: false},
		},
	}
	handler := func(context.Context, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok"}, nil
	}
	require.NoError(t, provider.Compute.RegisterNewService(schema, handler))

	consumer.AddPeer(provider.Config.ID, protocol.AddressRecord{Addresses: []string{provider.Config.Address}})
	provider.AddPeer(consumer.Config.ID, protocol.AddressRecord{Addresses: []string{consumer.Config.Address}})

	query := protocol.DiscoveryQuery{
		Service:      "pdf-converter",
		SortStrategy: protocol.StrategyFastest,
	}

	_, _, discovered, err := consumer.RequestServiceToCluster(query)
	require.NoError(t, err)
	require.Equal(t, "file", discovered.Parameters["input"].Type, "file type must propagate through discovery")
	require.True(t, discovered.Parameters["input"].Required)
	require.Equal(t, "int", discovered.Parameters["quality"].Type, "non-file params must remain unchanged")
}

func TestFileParameterTypeValidatesAsString(t *testing.T) {
	t.Parallel()
	provider := NewServer(t, testutil.DefaultConfig(t, "file-validator"), nil)

	schema := protocol.ServiceSchema{
		Name: "compressor",
		Parameters: map[string]protocol.ServiceParameter{
			"input": {Type: "file", Required: true},
		},
	}
	handler := func(_ context.Context, payload map[string]any) (map[string]any, error) {
		return map[string]any{"received": payload["input"]}, nil
	}
	require.NoError(t, provider.Compute.RegisterNewService(schema, handler))

	// Valid: string path should pass validation (DispatchTask returns nil)
	validTask := protocol.TaskRequest{
		TaskID:  "file-valid",
		Service: "compressor",
		Payload: map[string]any{"input": "/vfs/document.pdf"},
	}
	err := provider.DispatchTask(provider.Config.Address, validTask)
	require.NoError(t, err, "file param with valid string path should pass validation")

	// Invalid: non-string value should be rejected by parameter validation
	invalidTask := protocol.TaskRequest{
		TaskID:  "file-invalid",
		Service: "compressor",
		Payload: map[string]any{"input": 12345},
	}
	err = provider.DispatchTask(provider.Config.Address, invalidTask)
	require.Error(t, err, "file param with non-string value should be rejected at validation")
	require.Contains(t, err.Error(), "400", "validation should return a 400 bad request")
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
	srv.AddPeer("peer1", protocol.AddressRecord{Addresses: []string{"https://fake:8080"}})
	start := time.Now()
	err := srv.ExecuteSync()
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
			case <-time.After(20 * time.Second):
				return map[string]protocol.IndexEntry{}, nil
			}
		},
	}

	srv := NewServer(t, cfg, mockClient)
	srv.AddPeer("slow-peer", protocol.AddressRecord{Addresses: []string{"https://fake-address:8080"}})
	start := time.Now()

	err := srv.ExecuteSync()
	require.NoError(t, err)

	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 10*time.Second, "Exited too early, didn't wait for timeout")
	require.Less(t, duration, 11*time.Second, "Hung too long, failed to respect context timeout")
}

func TestUnauthorizedAccessIsRejectedAndPairingIsAllowed(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"), nil)

	clientWithoutCert := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	t.Run("Protected routes reject naked clients", func(t *testing.T) {
		resp, err := clientWithoutCert.Get(sv.Config.Address + "/peers")
		require.NoError(t, err, "TLS handshake should be successful because of the VerifyClientCertIfGiven")
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "The middleware should reject the access with the status 403 Forbidden")
	})

	t.Run("Pairing route allows naked clients", func(t *testing.T) {
		resp, err := clientWithoutCert.Get(sv.Config.Address + "/cluster/join")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.NotEqual(t, http.StatusForbidden, resp.StatusCode, "The middleware mTLSGuard should let the petition through to the pairing endpoint")
	})
}

func TestListenAndServeAndGracefulShutdown(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "listen-test")

	// Set port "0" to allow the os to select any available port.
	cfg.Address = "https://127.0.0.1:0"

	caPath := filepath.Dir(cfg.StoragePath)
	require.NoError(t, p2p.InitCluster(caPath))
	require.NoError(t, p2p.IssueNodeCertificate(caPath, cfg.StoragePath, cfg.ID))

	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(cfg.StoragePath, cfg.ID+".crt")
	nodeKeyFile := filepath.Join(cfg.StoragePath, cfg.ID+".key")
	serverTLS, _, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)

	srv := server.New(cfg, nil)

	serverErr := make(chan error, 1)

	go func() {
		serverErr <- srv.ListenAndServe(serverTLS)
	}()

	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = srv.Shutdown(ctx)
	require.NoError(t, err, "Node shutdown should run without errors")

	select {
	case err := <-serverErr:
		require.ErrorIs(t, err, http.ErrServerClosed, "ListenAndServe should have returned http.ErrServerClosed after shutdown")
	case <-time.After(1 * time.Second):
		t.Fatal("ListenAndServe was stuck and did not return after the shutdown")
	}
}

func TestInviteAndJoinLifecycle(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "sponsor"), nil)
	reqBody := server.InviteRequest{ValidForMinutes: 15}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", sv.Config.Address+"/peers/invite", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "Should have created a token successfully")

	var inviteResp server.InviteResponse
	err = json.NewDecoder(resp.Body).Decode(&inviteResp)
	require.NoError(t, err)

	_, secret, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)
	nakedClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	dummyNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer dummyNode.Close()
	validAddress := dummyNode.URL

	t.Run("Rejects an invalid token", func(t *testing.T) {
		badJoinReq := protocol.JoinRequest{Secret: "false-token-123", CSR: "dummy-csr", Address: validAddress}
		badBody, _ := json.Marshal(badJoinReq)

		respBad, err := nakedClient.Post(sv.Config.Address+"/cluster/join", "application/json", bytes.NewBuffer(badBody))
		require.NoError(t, err)
		defer func() { _ = respBad.Body.Close() }()

		require.Equal(t, http.StatusUnauthorized, respBad.StatusCode, "Invalid token should return 401")
	})

	t.Run("Accepts a valid token and deletes it after one use", func(t *testing.T) {
		goodJoinReq := protocol.JoinRequest{Secret: secret, CSR: "dummy-csr", Address: validAddress}
		goodBody, _ := json.Marshal(goodJoinReq)

		respGood, err := nakedClient.Post(sv.Config.Address+"/cluster/join", "application/json", bytes.NewBuffer(goodBody))
		require.NoError(t, err)
		defer func() { _ = respGood.Body.Close() }()

		require.Equal(t, http.StatusInternalServerError, respGood.StatusCode, "Token accepted, fails to sign false CSR")

		respReused, err := nakedClient.Post(sv.Config.Address+"/cluster/join", "application/json", bytes.NewBuffer(goodBody))
		require.NoError(t, err)
		defer func() { _ = respReused.Body.Close() }()

		require.Equal(t, http.StatusUnauthorized, respReused.StatusCode, "Token should have been deleted after one use")
	})
}

func TestNodeAnnounceAndSyncPropagation(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-node"), nil)
	newcomer := NewServer(t, testutil.DefaultConfig(t, "newcomer-node"), nil)

	fileName := "sync_target.txt"
	sponsor.Storage.SetSubscription(fileName, true)
	newcomer.Storage.SetSubscription(fileName, true)

	fileContent := "Data that newcomer needs to download"
	expectedHash := UploadFileSimulated(t, sponsor, fileName, fileContent)

	_, exists := newcomer.Storage.GetFileMeta(fileName)
	require.False(t, exists, "Newcomer shouldn't have the file yet")

	err := newcomer.AnnouncePresence(sponsor.Config.Address)
	require.NoError(t, err, "The announce shouldn't fail")

	peersOfSponsor := sponsor.GetPeersCopy()
	expectedPeersOfSponsor := map[string]string{newcomer.Config.ID: newcomer.Config.Address}
	expectedPeersOfNewcomer := map[string]string{sponsor.Config.ID: sponsor.Config.Address}
	require.Exactly(t, peersOfSponsor, expectedPeersOfSponsor, "Sponsor should have only registered newcomer as peer")

	peersOfNewcomer := newcomer.GetPeersCopy()
	require.Exactly(t, peersOfNewcomer, expectedPeersOfNewcomer, "Newcomer should have only registered sponsor as peer")

	err = newcomer.ExecuteSync()
	require.NoError(t, err, "ExecuteSync shouldn't return error")

	require.Eventually(t, func() bool {
		// force this 'Eventually' block to wait until the metadate is available, then
		// proceed to wait until the physicalBlob is available. This is necessary to prevent
		// a race condition between this block and 'assertRemoteHashToBeTheSameAs'
		meta, exists := newcomer.Storage.GetFileMeta(fileName)
		if !exists || meta.Hash != expectedHash {
			return false
		}
		hasBlob, _ := newcomer.Storage.HasPhysicalBlob(expectedHash)
		return hasBlob
	}, 3*time.Second, 100*time.Millisecond, "Newcomer should have synchronized with sponsor")

	assertRemoteHashToBeTheSameAs(t, expectedHash, fileContent, newcomer)
}

func TestDownloadWorkerProcessesDeletion(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-delete-worker")

	fileName := "worker_delete_target.txt"
	fileContent := "this file will be deleted via the download worker"
	expectedHash := testutil.CalculateHash(t, fileContent)

	// The manifest is shared by reference so we can mutate it between sync phases
	manifest := map[string]protocol.IndexEntry{
		fileName: {Name: fileName, Hash: expectedHash, Version: 1, Size: int64(len(fileContent))},
	}

	mockClient := &testutil.MockPeerClient{
		OnFetchManifest: func(ctx context.Context, addr string) (map[string]protocol.IndexEntry, error) {
			return manifest, nil
		},
		OnDownloadBlob: func(ctx context.Context, addr, hash string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(fileContent))), nil
		},
	}

	srv := NewServer(t, cfg, mockClient)
	srv.Storage.SetSubscription(fileName, true)
	srv.AddPeer("peer1", protocol.AddressRecord{Addresses: []string{"https://fake:8080"}})

	// Phase 1: Sync the file so the blob exists locally
	err := srv.ExecuteSync()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		hasBlob, _ := srv.Storage.HasPhysicalBlob(expectedHash)
		return hasBlob
	}, 3*time.Second, 100*time.Millisecond, "File should have been downloaded")

	// Phase 2: Peer sends a deletion notification (tombstone) via the real-time path.
	// This simulates what happens when a peer calls notifyPeers after deleting a file.
	// HandleNotification → vfs.Upsert updates metadata, but since File.Deleted=true
	// and the handler has !notification.File.Deleted, it won't enqueue a download.
	// So we must verify the metadata tombstone is applied.
	tombstone := protocol.IndexEntry{
		Name: fileName, Hash: expectedHash, Version: 2, Deleted: true,
	}
	srv.Storage.ProcessRemoteDeletion(tombstone)

	meta, exists := srv.Storage.GetFileMeta(fileName)
	require.True(t, exists, "Metadata entry should still exist after deletion")
	require.True(t, meta.Deleted, "Metadata should be marked as deleted")
	require.Equal(t, 2, meta.Version, "Version should have been incremented to 2")

	hasBlob, _ := srv.Storage.HasPhysicalBlob(expectedHash)
	require.False(t, hasBlob, "Physical blob should have been removed by ProcessRemoteDeletion")
}

func TestExpiredInviteIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "invite-expiry"), nil)

	// Generate a real invite
	reqBody := server.InviteRequest{ValidForMinutes: 15}
	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", sv.Config.Address+"/peers/invite", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var inviteResp server.InviteResponse
	err = json.NewDecoder(resp.Body).Decode(&inviteResp)
	require.NoError(t, err)

	_, secret, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)

	// Force the invite to expire
	sv.ExpireInvite(secret)

	// Attempt to join with the expired token
	dummyNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer dummyNode.Close()

	nakedClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	joinReq := protocol.JoinRequest{Secret: secret, CSR: "dummy-csr", Address: dummyNode.URL}
	joinBody, _ := json.Marshal(joinReq)

	respJoin, err := nakedClient.Post(sv.Config.Address+"/cluster/join", "application/json", bytes.NewBuffer(joinBody))
	require.NoError(t, err)
	defer func() { _ = respJoin.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, respJoin.StatusCode, "Expired token should return 401 Unauthorized")

	// The token should have been consumed (deleted), so using it again should also be rejected
	respReused, err := nakedClient.Post(sv.Config.Address+"/cluster/join", "application/json", bytes.NewBuffer(joinBody))
	require.NoError(t, err)
	defer func() { _ = respReused.Body.Close() }()

	require.Equal(t, http.StatusUnauthorized, respReused.StatusCode, "Token should have been deleted after the expired attempt")
}

func TestSnapshotReflectsFullClusterState(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*TestServer, clusterSize)
	for i := range clusterSize {
		servers[i] = NewServer(t, testutil.DefaultConfig(t, fmt.Sprintf("snap-%d", i)), nil)
	}

	// Full mesh connection
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.Config.ID, protocol.AddressRecord{Addresses: []string{peer.Config.Address}})
			}
		}
	}

	type testFile struct {
		Name    string
		Content string
	}
	files := []testFile{
		{"snapshot_a.txt", "content from node 0"},
		{"snapshot_b.txt", "content from node 1"},
		{"snapshot_c.txt", "content from node 2"},
	}

	// All nodes subscribe to all files
	for _, srv := range servers {
		for _, f := range files {
			srv.Storage.SetSubscription(f.Name, true)
		}
	}

	// Each node uploads its own file
	expectedHashes := make(map[string]string)
	for i, f := range files {
		expectedHashes[f.Name] = UploadFileSimulated(t, servers[i], f.Name, f.Content)
	}

	// Wait for all nodes to have all files in their manifests
	require.Eventually(t, func() bool {
		for _, srv := range servers {
			manifest := RequestManifestSimulated(t, srv)
			for _, f := range files {
				entry, exists := manifest[f.Name]
				if !exists || entry.Hash != expectedHashes[f.Name] {
					return false
				}
			}
		}
		return true
	}, 5*time.Second, 200*time.Millisecond, "All nodes should have all 3 files in their manifests")
}

func TestHTTPErrorResponses(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "err-node"), nil)

	tests := []struct {
		name           string
		method         string
		path           string
		body           io.Reader
		contentType    string
		expectedStatus int
	}{
		{
			name:           "subscribe without name",
			method:         http.MethodPost,
			path:           "/subscribe",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "delete without name",
			method:         http.MethodDelete,
			path:           "/file",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "upload without file part",
			method:         http.MethodPost,
			path:           "/upload",
			body:           invalidMultipartWithoutFile(t),
			contentType:    "multipart/form-data; boundary=xxx",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "notify with invalid JSON",
			method:         http.MethodPost,
			path:           "/notify",
			body:           strings.NewReader("{invalid}"),
			contentType:    "application/json",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != nil {
				body = tt.body
			}
			req, err := http.NewRequest(tt.method, sv.Config.Address+tt.path, body)
			require.NoError(t, err)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}

			resp, err := sv.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.expectedStatus, resp.StatusCode, "unexpected status for %s", tt.name)
		})
	}
}

func TestServerLoadsLocalServicesOnStartup(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "startup-services")

	// Create a dummy services.json in the storage path
	servicesFile := filepath.Join(cfg.StoragePath, "services.json")
	mockServices := `{
		"test-script": {
			"type": "script",
			"exec": "python3 dummy.py",
			"schema": {
				"name": "test-script",
				"description": "A dummy test script",
				"parameters": {
					"p1": {"type": "string", "required": true}
				}
			}
		}
	}`
	require.NoError(t, os.WriteFile(servicesFile, []byte(mockServices), 0644))

	// Initialize the server, which should load local services
	srv := server.New(cfg, nil)
	srv.LoadLocalServices()

	schema, exists := srv.Compute.GetService("test-script")
	require.True(t, exists, "Server should have loaded 'test-script' from services.json")
	require.Equal(t, "A dummy test script", schema.Description)
	require.Equal(t, "string", schema.Parameters["p1"].Type)
}

func TestServerHandlesServiceNotifications(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "service-notify-node")
	srv := server.New(cfg, nil)

	notification := protocol.ServiceNotification{
		Action: "add",
		NodeID: "peer-99",
		Schema: protocol.ServiceSchema{
			Name:        "test-svc",
			Description: "desc",
		},
	}

	bodyBytes, _ := json.Marshal(notification)
	req, _ := http.NewRequest(http.MethodPost, srv.Config.Address+"/services/notify", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	// Create a recorder
	recorder := httptest.NewRecorder()
	srv.HandleServiceNotify(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)

	services := srv.GetClusterServices("peer-99")
	require.Len(t, services, 1)
	require.Equal(t, "desc", services["test-svc"].Description)

	// Test remove
	notification.Action = "remove"
	bodyBytes, _ = json.Marshal(notification)
	req, _ = http.NewRequest(http.MethodPost, srv.Config.Address+"/services/notify", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	recorder = httptest.NewRecorder()
	srv.HandleServiceNotify(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	services = srv.GetClusterServices("peer-99")
	require.Len(t, services, 0)
}

func TestAnnounceCapturesPublicIP(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-stun"), nil)

	// Create a request pretending to come from a different IP
	announceReq := protocol.AddPeerRequest{
		ID: "new-stun-node",
		Address: protocol.AddressRecord{
			Addresses: []string{"https://192.168.1.99:8443"},
			Sequence:  1,
		},
	}

	bodyBytes, _ := json.Marshal(announceReq)
	req, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/announce", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	// Because we use a real http client, r.RemoteAddr will be 127.0.0.1:something
	// We expect the server to detect 127.0.0.1 and add it to the addresses
	resp, err := sponsor.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var peers map[string]protocol.AddressRecord
	err = json.NewDecoder(resp.Body).Decode(&peers)
	require.NoError(t, err)

	// the newcomer should be in the peers map, and its address list should include the local IP AND the perceived IP
	newcomerRecord := peers["new-stun-node"]
	require.Len(t, newcomerRecord.Addresses, 2, "Server should have added the perceived IP to the address record")

	hasPerceivedIP := false
	for _, addr := range newcomerRecord.Addresses {
		if strings.Contains(addr, "127.0.0.1:8443") {
			hasPerceivedIP = true
		}
	}
	require.True(t, hasPerceivedIP, "The perceived STUN-like IP should be in the address list")
}

func TestNodeIPChangeUpdatesPeers(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-update"), nil)

	// First announce
	req1 := protocol.AddPeerRequest{
		ID: "dynamic-node",
		Address: protocol.AddressRecord{
			Addresses: []string{"https://10.0.0.1:8443"},
			Sequence:  1,
		},
	}
	body1, _ := json.Marshal(req1)
	httpReq1, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/announce", bytes.NewBuffer(body1))
	httpReq1.Header.Set("Content-Type", "application/json")
	resp1, err := sponsor.Client().Do(httpReq1)
	require.NoError(t, err)
	_ = resp1.Body.Close()

	// Verify it was added
	var peers1 map[string]protocol.AddressRecord
	require.NoError(t, json.Unmarshal([]byte(GetPeersSimulated(t, sponsor)), &peers1))
	record1 := peers1["dynamic-node"]
	require.Contains(t, record1.Addresses[0], "10.0.0.1")

	// IP changes, sequence increments
	req2 := protocol.AddPeerRequest{
		ID: "dynamic-node",
		Address: protocol.AddressRecord{
			Addresses: []string{"https://20.0.0.2:8443"},
			Sequence:  2,
		},
	}
	body2, _ := json.Marshal(req2)
	httpReq2, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/announce", bytes.NewBuffer(body2))
	httpReq2.Header.Set("Content-Type", "application/json")
	resp2, err := sponsor.Client().Do(httpReq2)
	require.NoError(t, err)
	_ = resp2.Body.Close()

	// Verify it was updated
	var peers2 map[string]protocol.AddressRecord
	require.NoError(t, json.Unmarshal([]byte(GetPeersSimulated(t, sponsor)), &peers2))
	record2 := peers2["dynamic-node"]
	require.Equal(t, int64(2), record2.Sequence)

	// Ensure the new address is present
	hasNewIP := false
	for _, addr := range record2.Addresses {
		if strings.Contains(addr, "20.0.0.2") {
			hasNewIP = true
		}
	}
	require.True(t, hasNewIP, "The updated IP should be in the addresses list")

	// Old IP shouldn't be there if we overwrite correctly on higher sequence
	hasOldIP := false
	for _, addr := range record2.Addresses {
		if strings.Contains(addr, "10.0.0.1") {
			hasOldIP = true
		}
	}
	require.False(t, hasOldIP, "The old IP should be removed on a higher sequence update")
}

func TestAddPeerIgnoresOldSequence(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-seq"), nil)

	// Add sequence 2
	sponsor.AddPeer("dynamic-node", protocol.AddressRecord{
		Addresses: []string{"https://10.0.0.1:8443"},
		Sequence:  2,
	})

	// Add sequence 1
	sponsor.AddPeer("dynamic-node", protocol.AddressRecord{
		Addresses: []string{"https://10.0.0.2:8443"},
		Sequence:  1,
	})

	var peers map[string]protocol.AddressRecord
	require.NoError(t, json.Unmarshal([]byte(GetPeersSimulated(t, sponsor)), &peers))
	record := peers["dynamic-node"]
	require.Equal(t, int64(2), record.Sequence)
	require.Contains(t, record.Addresses, "https://10.0.0.1:8443")
	require.NotContains(t, record.Addresses, "https://10.0.0.2:8443")
}

func TestAnnouncePresenceFallbackError(t *testing.T) {
	t.Parallel()
	mockClient := &testutil.MockPeerClient{
		OnAnnounce: func(sponsor string, req protocol.AddPeerRequest) (map[string]protocol.AddressRecord, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	srv := NewServer(t, testutil.DefaultConfig(t, "fallback-test"), mockClient)

	err := srv.AnnouncePresence("https://unreachable:8443")
	require.Error(t, err)
	require.Contains(t, err.Error(), "there was an error trying to connect to the cluster")
}

func TestOfflineNotificationAndSelfHealing(t *testing.T) {
	t.Parallel()
	mockClient := &testutil.MockPeerClient{
		OnOffline: func(ctx context.Context, peerID string, req map[string]string) error {
			return nil
		},
	}
	srv1 := NewServer(t, testutil.DefaultConfig(t, "node1"), mockClient)
	srv2 := NewServer(t, testutil.DefaultConfig(t, "node2"), nil)

	srv1.AddPeer(srv2.Config.ID, protocol.AddressRecord{Addresses: []string{srv2.Config.Address}})
	srv1.SetPeerOnline(srv2.Config.ID, true)
	require.True(t, srv1.IsPeerOnline(srv2.Config.ID))

	offlineReq := struct {
		ID string `json:"id"`
	}{ID: srv2.Config.ID}
	body, _ := json.Marshal(offlineReq)
	req, err := http.NewRequest("POST", srv1.Config.Address+"/peers/offline", bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv1.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.False(t, srv1.IsPeerOnline(srv2.Config.ID), "Node2 should be marked offline")

	peersRecord := srv1.GetPeersCopy()
	_, exists := peersRecord[srv2.Config.ID]
	require.True(t, exists, "Node2 should not be deleted from the peer registry")

	srv1.AddPeer(srv2.Config.ID, protocol.AddressRecord{Addresses: []string{srv2.Config.Address}})
	require.True(t, srv1.IsPeerOnline(srv2.Config.ID), "Node2 should be marked online after reconnecting / sending announce")
}

func TestSponsorRegistryAndDiscovery(t *testing.T) {
	t.Parallel()
	srv := NewServer(t, testutil.DefaultConfig(t, "local-node"), nil)

	// Peer 1 is a Sponsor
	srv.AddPeer("sponsor-peer", protocol.AddressRecord{
		Addresses: []string{"https://10.0.0.5:8443"},
		Sequence:  1,
		IsSponsor: true,
	})

	// Peer 2 is not a Sponsor
	srv.AddPeer("regular-peer", protocol.AddressRecord{
		Addresses: []string{"https://10.0.0.6:8443"},
		Sequence:  1,
		IsSponsor: false,
	})

	sponsors := srv.GetSponsorPeers()
	require.Len(t, sponsors, 1)
	require.Contains(t, sponsors, "sponsor-peer")
	require.Equal(t, "https://10.0.0.5:8443", sponsors["sponsor-peer"])
	require.NotContains(t, sponsors, "regular-peer")
}

func TestProbeEndpoint(t *testing.T) {
	t.Parallel()
	srv := NewServer(t, testutil.DefaultConfig(t, "probe-server"), nil)

	parsed, err := url.Parse(srv.Config.Address)
	require.NoError(t, err)

	// Test 1: Reachable port
	probeReq := protocol.ProbeRequest{
		Address: parsed.Host,
	}
	bodyBytes, _ := json.Marshal(probeReq)
	resp, err := srv.Client().Post(srv.Config.Address+"/peers/probe", "application/json", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var probeResp protocol.ProbeResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&probeResp))
	require.True(t, probeResp.Reachable, "Should be reachable")

	// Test 2: Unreachable port (a port that is closed, e.g. 9999)
	probeReq2 := protocol.ProbeRequest{
		Address: "127.0.0.1:9999",
	}
	bodyBytes2, _ := json.Marshal(probeReq2)
	resp2, err := srv.Client().Post(srv.Config.Address+"/peers/probe", "application/json", bytes.NewBuffer(bodyBytes2))
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	var probeResp2 protocol.ProbeResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&probeResp2))
	require.False(t, probeResp2.Reachable, "Should be unreachable")
	require.NotEmpty(t, probeResp2.Error)
}

func TestDetermineSponsorAndNATStatus(t *testing.T) {
	t.Parallel()

	t.Run("manual override true", func(t *testing.T) {
		isSponsorOverride := true
		cfg := testutil.DefaultConfig(t, "override-true")
		cfg.IsSponsorOverride = &isSponsorOverride

		srv := NewServer(t, cfg, nil)
		srv.CheckNAT()
		require.True(t, srv.IsSponsorNode())
	})

	t.Run("manual override false", func(t *testing.T) {
		isSponsorOverride := false
		cfg := testutil.DefaultConfig(t, "override-false")
		cfg.IsSponsorOverride = &isSponsorOverride

		srv := NewServer(t, cfg, nil)
		srv.CheckNAT()
		require.False(t, srv.IsSponsorNode())
	})

	t.Run("auto-detect behind CGNAT", func(t *testing.T) {
		// Mock STUN server that returns a loopback IP (which is private/CGNAT)
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		stunAddr := conn.LocalAddr().String()

		go func() {
			buf := make([]byte, 1024)
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 {
				return
			}

			// Respond with loopback IP (127.0.0.1) as mapped address
			resp := make([]byte, 32)
			binary.BigEndian.PutUint16(resp[0:2], 0x0101) // Success Response
			binary.BigEndian.PutUint16(resp[2:4], 12)     // Length
			binary.BigEndian.PutUint32(resp[4:8], 0x2112A442)
			copy(resp[8:20], buf[8:20])

			binary.BigEndian.PutUint16(resp[20:22], 0x0001) // MAPPED-ADDRESS
			binary.BigEndian.PutUint16(resp[22:24], 8)
			resp[24] = 0
			resp[25] = 1 // IPv4
			binary.BigEndian.PutUint16(resp[26:28], uint16(raddr.Port))
			binary.BigEndian.PutUint32(resp[28:32], binary.BigEndian.Uint32(net.ParseIP("127.0.0.1").To4()))

			_, _ = conn.WriteToUDP(resp, raddr)
		}()

		cfg := testutil.DefaultConfig(t, "auto-detect-node")
		cfg.STUNServer = stunAddr

		srv := NewServer(t, cfg, nil)
		srv.CheckNAT()
		// Since STUN returned 127.0.0.1 which is in private/loopback range, it should auto-detect as NOT a Sponsor
		require.False(t, srv.IsSponsorNode())
	})
}

func TestAnnounceEndpointEnforcesMTLS(t *testing.T) {
	srv := NewServer(t, testutil.DefaultConfig(t, "sponsor-node"), nil)

	// Make an HTTP POST request to /peers/announce WITHOUT client certificates
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}

	reqBody := protocol.AddPeerRequest{
		ID: "malicious-node",
		Address: protocol.AddressRecord{
			Addresses: []string{"https://127.0.0.1:9999"},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)
	resp, err := client.Post(srv.Config.Address+"/peers/announce", "application/json", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Verify that it was rejected with StatusForbidden (403)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestInviteSweeperRemovesExpiredTokens(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-sweep"), nil)

	// 1. Generate an invite
	bodyBytes, _ := json.Marshal(map[string]interface{}{"role": "node"})
	req, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/invite", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := sponsor.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var inviteResp server.InviteResponse
	err = json.NewDecoder(resp.Body).Decode(&inviteResp)
	require.NoError(t, err)
	require.NotEmpty(t, inviteResp.Token)

	// Parse the smart token to get the secretHex
	_, secretHex1, err := p2p.ParseSmartToken(inviteResp.Token)
	require.NoError(t, err)

	// Verify the invite is active initially
	_, validBeforeExpire := sponsor.Invites.CheckAndConsume(secretHex1)
	require.True(t, validBeforeExpire, "Invite token should be valid before expiration")

	// 2. Generate another invite and force it to be expired
	req2, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/invite", bytes.NewBuffer(bodyBytes))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := sponsor.Client().Do(req2)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp2.StatusCode)

	var inviteResp2 server.InviteResponse
	err = json.NewDecoder(resp2.Body).Decode(&inviteResp2)
	require.NoError(t, err)
	require.NotEmpty(t, inviteResp2.Token)

	_, secretHex2, err := p2p.ParseSmartToken(inviteResp2.Token)
	require.NoError(t, err)

	// Expire the second invite using the TestServer helper
	sponsor.ExpireInvite(secretHex2)

	// 3. Run Sweep manually to cleanup expired invites
	sponsor.Invites.Sweep()

	// Verify the expired invite is gone/invalid
	_, validAfterExpire := sponsor.Invites.CheckAndConsume(secretHex2)
	require.False(t, validAfterExpire, "Expired invite token should have been removed by Sweep")
}

func TestPeerLeavesClusterGracefullyAndNotifiesOthers(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-leave"), nil)
	peer1 := NewServer(t, testutil.DefaultConfig(t, "peer-1"), nil)
	peer2 := NewServer(t, testutil.DefaultConfig(t, "peer-2"), nil)

	// Interconnect Peer 1 with Sponsor
	sponsor.AddPeer(peer1.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer1.Config.Address},
		IsSponsor: false,
	})
	peer1.AddPeer(sponsor.Config.ID, protocol.AddressRecord{
		Addresses: []string{sponsor.Config.Address},
		IsSponsor: true,
	})

	// Verify Peer 1 is in active registry on Sponsor
	require.Contains(t, sponsor.GetPeersCopy(), peer1.Config.ID)

	// 1. Peer 2 announces to Sponsor, then goes offline (before CA rotation occurs)
	sponsor.AddPeer(peer2.Config.ID, protocol.AddressRecord{
		Addresses: []string{peer2.Config.Address},
		IsSponsor: false,
	})
	require.Contains(t, sponsor.GetPeersCopy(), peer2.Config.ID)
	require.True(t, sponsor.IsPeerOnline(peer2.Config.ID))

	offlineReq := struct {
		ID string `json:"id"`
	}{ID: peer2.Config.ID}
	bodyBytesOffline, _ := json.Marshal(offlineReq)
	reqOffline, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/offline", bytes.NewBuffer(bodyBytesOffline))
	reqOffline.Header.Set("Content-Type", "application/json")
	respOffline, err := sponsor.Client().Do(reqOffline)
	require.NoError(t, err)
	defer func() { _ = respOffline.Body.Close() }()
	require.Equal(t, http.StatusOK, respOffline.StatusCode)

	// Verify Peer 2 is marked as offline on Sponsor
	require.False(t, sponsor.IsPeerOnline(peer2.Config.ID))

	// 2. Peer 1 requests graceful leave from Sponsor (which triggers async CA rotation)
	leaveReq := struct {
		ID string `json:"id"`
	}{ID: peer1.Config.ID}
	bodyBytes, _ := json.Marshal(leaveReq)
	req, _ := http.NewRequest(http.MethodPost, sponsor.Config.Address+"/peers/leave", bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := sponsor.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify Peer 1 is removed from Sponsor's registry
	require.NotContains(t, sponsor.GetPeersCopy(), peer1.Config.ID)
}

func TestTelemetryEndpointReportsBandwidthAndResourceUsage(t *testing.T) {
	t.Parallel()
	sponsor := NewServer(t, testutil.DefaultConfig(t, "sponsor-telemetry"), nil)

	// 1. Record some bandwidth activity
	sponsor.Bandwidth.RecordBytesSent(1024, "/download/somehash")
	sponsor.Bandwidth.RecordBytesReceived(2048, "/upload")

	// Verify categories are populated
	categorySent := sponsor.Bandwidth.CategorizePath("/download/somehash")
	require.Equal(t, "vfs:somehash", categorySent)

	upSpeed, downSpeed := sponsor.GetCurrentBandwidth()
	require.Equal(t, 1024.0/5.0, upSpeed)
	require.Equal(t, 2048.0/5.0, downSpeed)

	totalSent, totalRecv := sponsor.GetTotalBandwidth()
	require.Equal(t, int64(1024), totalSent)
	require.Equal(t, int64(2048), totalRecv)

	// Verify Category Bandwidth
	vfsSentSpeed, _ := sponsor.Bandwidth.GetCategoryBandwidth("vfs:somehash")
	require.Equal(t, 1024.0/5.0, vfsSentSpeed)

	// 2. Query the HTTP /telemetry endpoint
	req, _ := http.NewRequest(http.MethodGet, sponsor.Config.Address+"/telemetry", nil)
	resp, err := sponsor.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var telemetryResp map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&telemetryResp)
	require.NoError(t, err)

	require.Equal(t, sponsor.Config.ID, telemetryResp["node_id"])
	require.Contains(t, telemetryResp, "cpu_limit")
	require.Contains(t, telemetryResp, "memory_limit")

	// 3. Verify LocalBandwidthStats used by local Unix socket calls
	stats := sponsor.LocalBandwidthStats()
	require.Equal(t, int64(1024), stats.TotalSent)
	require.Equal(t, int64(2048), stats.TotalReceived)
}

func TestPeerPersistenceAndStatus(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "persisted_node")

	// Create a new server instance. It will initialize BoltDB.
	srv1 := NewServer(t, cfg, nil)

	peerID := "some-peer"
	addrRec := protocol.AddressRecord{
		Addresses: []string{"https://127.0.0.1:9090"},
		IsSponsor: false,
		Sequence:  12345,
	}

	// Add peer. This should automatically save the peer to BoltDB.
	srv1.AddPeer(peerID, addrRec)

	// Verify it's online initially in srv1
	require.True(t, srv1.IsPeerOnline(peerID))

	// Simulate connection failure (marking peer offline with an error)
	srv1.SetPeerOffline(peerID, fmt.Errorf("connection refused"))
	require.False(t, srv1.IsPeerOnline(peerID))
	require.Equal(t, "offline or could not reach: connection refused", srv1.Peers.GetPeerError(peerID))

	// Simulate peer sending offline signal
	srv1.SetPeerOnline(peerID, false)
	require.False(t, srv1.IsPeerOnline(peerID))
	require.Equal(t, "offline", srv1.Peers.GetPeerError(peerID))

	// Shutdown srv1 so we can start srv2 using the same directory
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := srv1.Shutdown(ctx)
	require.NoError(t, err)
	srv1.httpTestSrv.Close()

	// Now start a second server from the same directory
	// It should load the peer from BoltDB.
	cfg2 := cfg
	cfg2.ID = "persisted_node"
	srv2 := NewServer(t, cfg2, nil)

	// Check if peer list has been successfully reloaded from DB
	peersList := srv2.LocalPeersList()
	require.Len(t, peersList, 1)
	require.Equal(t, peerID, peersList[0].ID)
	require.False(t, peersList[0].Online)
	require.Equal(t, "offline or could not reach: not contacted yet", peersList[0].Error)

	// Verify removing peer deletes it from DB as well
	srv2.RemovePeer(peerID)

	// Shutdown srv2
	err = srv2.Shutdown(ctx)
	require.NoError(t, err)
	srv2.httpTestSrv.Close()

	// Start a third server and verify the peer is gone
	srv3 := NewServer(t, cfg2, nil)
	require.Len(t, srv3.LocalPeersList(), 0)
}
