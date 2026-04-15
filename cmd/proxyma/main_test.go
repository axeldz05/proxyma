package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testLogWriter struct {
	t *testing.T
}

func (w testLogWriter) Write(p []byte) (n int, err error) {
	w.t.Log(strings.TrimSpace(string(p))) 
	return len(p), nil
}

type TestServer struct {
	*server.Server
	httpTestSrv    *httptest.Server
}

func (ts *TestServer) Client() *http.Client {
	return ts.httpTestSrv.Client()
}

func (ts *TestServer) Close() {
	ts.httpTestSrv.Close()
	ts.Server.Close()
}

func DefaultConfigFor(t *testing.T, id string) protocol.NodeConfig {
	writer := testLogWriter{t: t}
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	handler := slog.NewTextHandler(writer, opts)
	testLogger := slog.New(handler).With("node", id)
	return protocol.NodeConfig{
		ID:          id,
		StoragePath: t.TempDir(),
		Workers: 2,
		Logger: testLogger,
	}
}

func NewServer(t *testing.T, cfg protocol.NodeConfig) *TestServer {
	caPath := filepath.Dir(cfg.StoragePath)
	serverTLS, clientTLS, _ := p2p.GenerateOrLoadTLSConfig(caPath, cfg.StoragePath, cfg.ID)
	
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
	}

	app := server.New(cfg, httpClient)
	ts := httptest.NewUnstartedServer(app.MountHandlers())
	ts.TLS = serverTLS
	ts.StartTLS()
	ts.Client().Transport = httpClient.Transport
	app.SetAddress(ts.URL)

	return &TestServer{
		Server:      app,
		httpTestSrv: ts,
	}
}

func CalculateHash(t *testing.T, content string) string {
	t.Helper()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}

func UploadFileSimulated(t *testing.T, sv *TestServer, fileName, content string) string {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = io.WriteString(fileWriter, content)
	require.NoError(t, err)
	writer.Close()

	reqUp, err := http.NewRequest("POST", sv.Config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())

	respUp, err := sv.Client().Do(reqUp)
	require.NoError(t, err)
	defer respUp.Body.Close()

	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return CalculateHash(t, content)
}

func DeleteFileSimulated(t *testing.T, sv *TestServer, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.Config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)

	respDel, err := sv.Client().Do(reqDel)
	require.NoError(t, err)
	defer respDel.Body.Close()

	require.Equal(t, http.StatusOK, respDel.StatusCode, "Delete should have return 200 OK")
}

func Test01FirstServerIsAlreadySynced(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()
	require.NoError(t, sv.Storage.SyncStorage(sv.GetPeersCopy()))
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

func Test02AServerCanConnectToAnother(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, DefaultConfigFor(t, "1"))
	sv2 := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv1.Close()
	defer sv2.Close()
	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)

	gotPeersOfSv2 := strings.TrimSpace(GetPeersSimulated(t, sv2))
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.Config.Address)
	gotPeersOfSv1 := strings.TrimSpace(GetPeersSimulated(t, sv1))
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.Config.Address)

	require.Equal(t, expectedPeersOfSv2, gotPeersOfSv2)
	require.Equal(t, expectedPeersOfSv1, gotPeersOfSv1)
	require.NoError(t, sv1.Storage.SyncStorage(sv1.GetPeersCopy()))
	require.NoError(t, sv2.Storage.SyncStorage(sv2.GetPeersCopy()))
}

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, updatedServer *TestServer) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.Config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	resp, err := updatedServer.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
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

func Test03AllServersSyncsToLastUpdated(t *testing.T) {
	t.Parallel()
	updatedServer := NewServer(t, DefaultConfigFor(t, "1"))
	noUpdatedServer := NewServer(t, DefaultConfigFor(t, "2"))
	noUpdatedServer2 := NewServer(t, DefaultConfigFor(t, "3"))
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()

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

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

	fileName := "test04.txt"
	fileContent := "testing"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	fileMeta, exists := sv.Storage.GetFileMeta(fileName)

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func Test05P2PNetworkEventualConsistency(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*TestServer, clusterSize)
	for i := 0; i < clusterSize; i++ {
		serverName := fmt.Sprintf("%d", i)
		servers[i] = NewServer(t, DefaultConfigFor(t, serverName))
		defer servers[i].Close()
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

func Test06DownloadEndpointUsesHashInsteadOfName(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()
	fileName := "test06.txt"
	fileContent := "Hello!!"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	downloadURL := fmt.Sprintf("%s/download/%s", sv.Config.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	respDL, err := sv.Client().Do(reqDL)
	require.NoError(t, err)
	defer respDL.Body.Close()

	require.Equal(t, http.StatusOK, respDL.StatusCode, "Server should answer with OK 200 status when requesting Hash")
	buf := new(strings.Builder)
	_, err = io.Copy(buf, respDL.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, buf.String(), "Downloaded content should be the same as the uploaded content")
}

func Test07NetworkRequestRespectsTimeouts(t *testing.T) {
	t.Parallel()
	// A "trap" node that takes 5 seconds to respond
	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowPeer.Close()

	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

	fakeFile := protocol.IndexEntry{
		Name: "trampa.txt",
		Size: 100,
		Hash: "hashfalso123",
	}
	
	// TODO: make a test function helper to reduce sections like these
	sv.Storage.SetSubscription(fakeFile.Name, true)
	notif := p2p.PeerNotification{
		File:   fakeFile,
		Source: slowPeer.URL,
	}
	body, _ := json.Marshal(notif)
	req, _ := http.NewRequest("POST", sv.Config.Address+"/notify", bytes.NewReader(body))

	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	time.Sleep(3 * time.Second)
	hasBlob, err := sv.Storage.HasPhysicalBlob(fakeFile.Hash)
	require.NoError(t, err)
	require.False(t, hasBlob, "The physical blob should not be saved if the download timed out")
}

func Test08UnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

	clientWithoutCert := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := clientWithoutCert.Get(sv.Config.Address + "/peers")
	require.Error(t, err, "Should fail at TLS handshake due to missing client certs")
	if resp != nil {
		resp.Body.Close()
	}
}

func Test09ManifestEndpointReturnsCurrentState(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

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
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "The endpoint /manifest must answer with status code: 200 OK")

	var manifest map[string]protocol.IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err, "The manifest must be a valid JSON in format: map[string]FileInfo")

	require.Contains(t, manifest[fakeFile.Name].Hash, fakeHash, "The manifest must contain the hash of the injected file")
	require.Equal(t, fakeFile.Name, manifest[fakeFile.Name].Name, "The filename must be the same as in the manifest")
}

func Test10SyncStorageDownloadsMissingFiles(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv1.Close()

	fileName := "missingFile.txt"
	fileContent := "helloo from test10"
	expectedHash := UploadFileSimulated(t, sv1, fileName, fileContent)

	sv2 := NewServer(t, DefaultConfigFor(t, "2"))
	defer sv2.Close()
	sv2.AddPeer("1", sv1.Config.Address)
	sv2.Storage.SetSubscription(fileName, true)

	_, existsBefore := sv2.Storage.GetFileMeta(fileName)

	require.False(t, existsBefore, "Node 2 shouldn't have any files")

	err := sv2.Storage.SyncStorage(sv2.GetPeersCopy())
	require.NoError(t, err, "Storage.SyncStorage shouldn't fail")
	require.Eventually(t, func() bool {
		fileMeta, existsAfter := sv2.Storage.GetFileMeta(fileName)
		if !existsAfter {
			return false
		}
		return fileMeta.Hash == expectedHash
	}, 2*time.Second, 100*time.Millisecond, "Node 2 should have the file of node 1 after executing Storage.SyncStorage")
}

func Test11VirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()
	fileName := "test11.txt"
	fileContent := "hello from test11"
	hash1 := UploadFileSimulated(t, sv, fileName, fileContent)
	fileContent2 := "goodbye from test11"
	hash2 := UploadFileSimulated(t, sv, fileName, fileContent2)

	meta, exists := sv.Storage.GetFileMeta(fileName)

	require.True(t, exists, "The system must track the file by its logic name")
	require.Equal(t, hash2, meta.Hash, "Index should point to the Version 2 Hash")
	require.NotEqual(t, hash1, meta.Hash, "Hash should have changed")
	require.Equal(t, 2, meta.Version, "Version of the file should have been incremented to 2")
}

func Test12WorkerPoolLimitsConcurrency(t *testing.T) {
	t.Parallel()
	mockFiles := make(map[string]string)
	var notifications []p2p.PeerNotification

	for i := range 5 {
		content := fmt.Sprintf("Content %d", i)
		hasher := sha256.New()
		hasher.Write([]byte(content))
		hash := hex.EncodeToString(hasher.Sum(nil))
		mockFiles[hash] = content
		fileName := fmt.Sprintf("file_%d.txt", i)
		notifications = append(notifications, p2p.PeerNotification{
			File: protocol.IndexEntry{Name: fileName, Hash: hash, Version: 1},
		})
	}

	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		requestedHash := strings.TrimPrefix(r.URL.Path, "/download/")
		if content, exists := mockFiles[requestedHash]; exists {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(content))
		}
	}))
	defer slowPeer.Close()

	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

	start := time.Now()

	for _, notif := range notifications {
		sv.Storage.SetSubscription(notif.File.Name, true)
		notif.Source = slowPeer.URL
		body, _ := json.Marshal(notif)
		req, _ := http.NewRequest("POST", sv.Config.Address+"/notify", bytes.NewReader(body))
		resp, err := sv.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
	}

	require.Eventually(t, func() bool {
		allContentIsDownloaded := true
		for _, v := range sv.Storage.GetVFSSnapshot() {
			var buf bytes.Buffer
			if err := sv.Storage.ReadPhysicalBlob(v.Hash, &buf); err != nil {
				return false
			}
			if expectedContent, exists := mockFiles[v.Hash]; exists{
				allContentIsDownloaded = allContentIsDownloaded && buf.String() == expectedContent
			} else {
				return false
			}
		}
		return len(sv.Storage.GetVFSSnapshot()) == 5 && allContentIsDownloaded
	}, 5*time.Second, 100*time.Millisecond)

	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 2*time.Second, "Too fast. The Worker Pool isn't limiting the concurrency.")
	require.Less(t, duration, 4*time.Second, "Too slow. System is working sequentially.")
}

func Test13LocalDeleteCreatesTombstone(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	defer sv.Close()

	fileName := "test13.txt"
	fileContent := "hello from test13!!"
	UploadFileSimulated(t, sv, fileName, fileContent)

	metaBefore, _ := sv.Storage.GetFileMeta(fileName)
	require.False(t, metaBefore.Deleted, "File should have not been deleted previously")
	
	DeleteFileSimulated(t, sv, fileName)
	metaAfter, exists := sv.Storage.GetFileMeta(fileName)

	require.True(t, exists, "The protocol.IndexEntry of the file should still exist after deleting")
	require.True(t, metaAfter.Deleted, "Deleted should be true in the protocol.IndexEntry")
	require.Equal(t, metaBefore.Version+1, metaAfter.Version, "Version should have been incremented")

	// TODO: avoid using blobExists and make another function in the main instead
	existsInDisk, _ := sv.Storage.HasPhysicalBlob(metaBefore.Hash)
	require.False(t, existsInDisk, "The physical blob should have been deleted")
}

func Test14TombstonePropagatesToPeers(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, DefaultConfigFor(t, "1"))
	sv2 := NewServer(t, DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

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

func Test15SelectiveSynchronization(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, DefaultConfigFor(t, "1"))
	sv2 := NewServer(t, DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

	fileAName := "fileA.txt"
	fileBName := "fileB.txt"

	UploadFileSimulated(t, sv1, fileAName, "Exclusive File A content")
	UploadFileSimulated(t, sv1, fileBName, "Another content, File B")

	// Sv2 subscribes ONLY to fileA via API
	reqSub, _ := http.NewRequest("POST", sv2.Config.Address+"/subscribe?name="+fileAName, nil)

	respSub, err := sv2.Client().Do(reqSub)
	require.NoError(t, err)
	defer respSub.Body.Close()
	require.Equal(t, http.StatusOK, respSub.StatusCode, "Subscribe API should return 200 OK")

	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)
	require.NoError(t, sv2.Storage.SyncStorage(sv2.GetPeersCopy()))

	// Verify sv2 gets file A
	require.Eventually(t, func() bool {
		_, exists := sv2.Storage.GetFileMeta(fileAName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have synced the subscribed file A")

	// Verify sv2 DOES NOT get file B
	time.Sleep(1 * time.Second) // wait to ensure no late sync happens
	metaB, existsB := sv2.Storage.GetFileMeta(fileBName)
	require.True(t, existsB, "Server2 SHOULD have the metadata of file B in its Storage.vfs")
	existsInDisk, _ := sv2.Storage.HasPhysicalBlob(metaB.Hash)
	require.False(t, existsInDisk, "Server2 should NOT download the physical blob of file B because it is not subscribed")}

func Test16mTLSConnectionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	clusterDir := t.TempDir()
	serverTLS, clientTLS, err := p2p.GenerateOrLoadTLSConfig(clusterDir, clusterDir, "legit-node")
	require.NoError(t, err, "Should not fail while generating certs for the cluster")
	handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hyper secure connection"))
	})

	handler := slog.NewTextHandler(testLogWriter{t: t}, &slog.HandlerOptions{Level: slog.LevelDebug})
	testSlog := slog.New(handler).With("node", "Test17-mTLS")
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
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("Reject clients without a cert", func(t *testing.T) {
		nakedClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}

		_, err := nakedClient.Get(secureServer.URL)
		require.Error(t, err, "The client should not be able to connect")
		require.Contains(t, err.Error(), "certificate required", "The server must require a certificate")
	})

	t.Run("Reject certificates from an unknown CA", func(t *testing.T) {
		hackerDir := t.TempDir()

		_, hackerClientTLS, err := p2p.GenerateOrLoadTLSConfig(hackerDir, hackerDir, "hacker-node")
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

func Test17NodeCannotRegisterDuplicateServices(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, DefaultConfigFor(t, "1"))
	
	savedParameters := map[string]protocol.ServiceParameter{
		"image": {Type: "string", Required: true},
		"language": {Type: "string", Required: false},
		"output": {Type: "string", Required: false},
	}
	schema1 := protocol.ServiceSchema{
		Name:        "ocr",
		Description: "Standard Optical Character Recognition",
		Parameters: savedParameters,
	}

	err := sv.Compute.RegisterNewService(schema1)
	require.NoError(t, err, "The first ServiceScheme should be registered")
	
	schemaImpostor := protocol.ServiceSchema{
		Name:        "ocr",
		Description: "A ocr impostor that should fail",
		Parameters: map[string]protocol.ServiceParameter{
			"image": {Type: "string", Required: true},
			"language": {Type: "string", Required: true},
			"output": {Type: "string", Required: true},
		},
	}

	err = sv.Compute.RegisterNewService(schemaImpostor)
	
	require.Error(t, err, "The Registry should reject repeated services")
	require.ErrorIs(t, err, compute.ErrServiceDuplicate)
	
	savedSchema, exists := sv.Compute.GetService("ocr")
	require.True(t, exists)
	require.Equal(t, "Standard Optical Character Recognition", savedSchema.Description)
	require.Equal(t, savedParameters, savedSchema.Parameters)
}

func Test18ANodeReceivesSatisfactoryAnswerFromServiceRequest(t *testing.T) {
	t.Parallel()
	svWithService := NewServer(t, DefaultConfigFor(t, "1"))
	svDemandingService := NewServer(t, DefaultConfigFor(t, "2"))
	defer svWithService.Close()
	defer svDemandingService.Close()

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
	err := svWithService.Compute.RegisterNewService(schema1)
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
		TaskID:   taskID,
		Service: "ocr",
		Payload:  filledInputs,
		ReplyTo: svDemandingService.Config.Address + "/services/callback",
	}

	err = svDemandingService.DispatchTask(targetPeerAddr, reqPayload)
	require.NoError(t, err, "The node worker should have accepted the task")

	require.Eventually(t, func() bool {
		taskResult, exists := svDemandingService.Compute.GetTaskStatus(taskID)
		return exists && taskResult.Status == "completed"
	}, 2*time.Second, 100*time.Millisecond, "The completion Webhook never arrived")
}
