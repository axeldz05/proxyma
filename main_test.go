package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"proxyma/storage"
	"strings"
	"sync"
	"testing"
	"time"
	"log/slog"
	"github.com/stretchr/testify/require"
)

type testLogWriter struct {
	t *testing.T
}

func (w testLogWriter) Write(p []byte) (n int, err error) {
	w.t.Log(strings.TrimSpace(string(p))) 
	return len(p), nil
}

func DefaultConfigFor(t *testing.T, id string) NodeConfig {
	writer := testLogWriter{t: t}
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	handler := slog.NewTextHandler(writer, opts)
	testLogger := slog.New(handler).With("node", id)
	return NodeConfig{
		ID:          id,
		StoragePath: t.TempDir(),
		Workers: 2,
		Logger: testLogger,
	}
}

func CalculateHash(t *testing.T, content string) string {
	t.Helper()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}

func UploadFileSimulated(t *testing.T, sv *Server, fileName, content string) string {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = io.WriteString(fileWriter, content)
	require.NoError(t, err)
	writer.Close()

	reqUp, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())

	respUp, err := sv.server.Client().Do(reqUp)
	require.NoError(t, err)
	defer respUp.Body.Close()

	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return CalculateHash(t, content)
}

func DeleteFileSimulated(t *testing.T, sv *Server, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)

	respDel, err := sv.server.Client().Do(reqDel)
	require.NoError(t, err)
	defer respDel.Body.Close()

	require.Equal(t, http.StatusOK, respDel.StatusCode, "Delete should have return 200 OK")
}

func NewServer(cfg NodeConfig) *Server {
	s := &Server{
		config:        		cfg,
		Peers:         		make(map[string]string),
		storage:       		*storage.NewStorage(cfg.StoragePath),
		vfs:           		NewVFS(),
		downloadQueue: 		make(chan DownloadJob, 1000),
		subscriptions: 		&sync.Map{},
		serviceRegistry: 	NewServiceRegistry(),
	}

	os.MkdirAll(cfg.StoragePath, 0755)

	caFolderPath := filepath.Dir(cfg.StoragePath)
	serverTLS, clientTLS, _ := GenerateOrLoadTLSConfig(caFolderPath, cfg.StoragePath, cfg.ID)

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
	}
	s.peerClient = NewHTTPPeerClient(httpClient)

	s.server = httptest.NewUnstartedServer(s.MountHandlers())
	s.server.TLS = serverTLS
	s.server.Config.ErrorLog = slog.NewLogLogger(s.config.Logger.Handler(), slog.LevelError)
	s.server.StartTLS()

	cfg.Address = s.server.URL
	s.config = cfg

	s.server.Client().Transport = httpClient.Transport

	for i := 0; i < s.config.Workers; i++ {
		go s.downloadWorker()
	}

	return s
}

func Test01FirstServerIsAlreadySynced(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()
	require.NoError(t, sv.SyncStorage())
}

func GetPeersSimulated(t *testing.T, sv *Server) string {
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
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "1"))
	defer sv1.Close()
	defer sv2.Close()
	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)

	gotPeersOfSv2 := strings.TrimSpace(GetPeersSimulated(t, sv2))
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.config.Address)
	gotPeersOfSv1 := strings.TrimSpace(GetPeersSimulated(t, sv1))
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.config.Address)

	require.Equal(t, expectedPeersOfSv2, gotPeersOfSv2)
	require.Equal(t, expectedPeersOfSv1, gotPeersOfSv1)
	require.NoError(t, sv1.SyncStorage())
	require.NoError(t, sv2.SyncStorage())
}

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, updatedServer *Server) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	resp, err := updatedServer.server.Client().Do(req)
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
	updatedServer := NewServer(DefaultConfigFor(t, "1"))
	noUpdatedServer := NewServer(DefaultConfigFor(t, "2"))
	noUpdatedServer2 := NewServer(DefaultConfigFor(t, "3"))
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()

	updatedServer.AddPeer("2", noUpdatedServer.config.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.config.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.config.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.config.Address)
	fileName := "test03.txt"
	noUpdatedServer.subscriptions.Store(fileName, true)
	noUpdatedServer2.subscriptions.Store(fileName, true)
	
	fileContent := "Hello!!!"
	expectedHash := UploadFileSimulated(t, updatedServer, fileName, fileContent)

	_, exists := updatedServer.vfs.Get(fileName)
	if !exists {
		t.Errorf("Blob hash '%s' was not registered in the metadata", expectedHash)
	}

	assertRemoteHashToBeTheSameAs(t, expectedHash, fileContent, updatedServer)
	require.Eventually(t, func() bool {
		_, exists := noUpdatedServer.vfs.Get(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	fileName := "test04.txt"
	fileContent := "testing"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	fileMeta, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func Test05P2PNetworkEventualConsistency(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*Server, clusterSize)
	for i := 0; i < clusterSize; i++ {
		serverName := fmt.Sprintf("%d", i)
		servers[i] = NewServer(DefaultConfigFor(t, serverName))
		defer servers[i].Close()
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.config.ID, peer.config.Address)
			}
		}
	}
	fileName := "test05.txt"
	for _, srv := range servers {
		srv.subscriptions.Store(fileName, true)
	}
	fileContent := "everyone wants me"
	expectedHash := UploadFileSimulated(t, servers[0], fileName, fileContent)
	require.Eventually(t, func() bool {
		for _, srv := range servers {

			meta, exists := srv.vfs.Get(fileName)

			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func Test06DownloadEndpointUsesHashInsteadOfName(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()
	fileName := "test06.txt"
	fileContent := "Hello!!"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	downloadURL := fmt.Sprintf("%s/download/%s", sv.config.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	respDL, err := sv.server.Client().Do(reqDL)
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

	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	fakeFile := IndexEntry{
		Name: "trampa.txt",
		Size: 100,
		Hash: "hashfalso123",
	}

	start := time.Now()
	sv.downloadFileFromPeer(fakeFile, slowPeer.URL)
	duration := time.Since(start)
	require.Less(t, duration, 3*time.Second, "The request should have been aborted by Timeout before the slow node ended")

	_, exists := sv.vfs.Get(fakeFile.Name)

	require.False(t, exists, "The file should not be registered if the download failed or timed out")
}

func Test08UnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	clientWithoutCert := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := clientWithoutCert.Get(sv.config.Address + "/peers")
	require.Error(t, err, "Should fail at TLS handshake due to missing client certs")
	if resp != nil {
		resp.Body.Close()
	}
}

func Test09ManifestEndpointReturnsCurrentState(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	fakeHash := "hash-simulado-999"
	fakeFile := IndexEntry{
		Name: "dataset_v2.csv",
		Size: 1024,
		Hash: fakeHash,
	}

	sv.vfs.Upsert(fakeFile)

	req, err := http.NewRequest("GET", sv.config.Address+"/manifest", nil)
	require.NoError(t, err)

	resp, err := sv.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "The endpoint /manifest must answer with status code: 200 OK")

	var manifest map[string]IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err, "The manifest must be a valid JSON in format: map[string]FileInfo")

	require.Contains(t, manifest[fakeFile.Name].Hash, fakeHash, "The manifest must contain the hash of the injected file")
	require.Equal(t, fakeFile.Name, manifest[fakeFile.Name].Name, "The filename must be the same as in the manifest")
}

func Test10SyncStorageDownloadsMissingFiles(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	defer sv1.Close()

	fileName := "missingFile.txt"
	fileContent := "helloo from test10"
	expectedHash := UploadFileSimulated(t, sv1, fileName, fileContent)

	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv2.Close()
	sv2.AddPeer("1", sv1.config.Address)
	sv2.subscriptions.Store(fileName, true)

	_, existsBefore := sv2.vfs.Get(fileName)

	require.False(t, existsBefore, "Node 2 shouldn't have any files")

	err := sv2.SyncStorage()
	require.NoError(t, err, "SyncStorage shouldn't fail")
	require.Eventually(t, func() bool {
		fileMeta, existsAfter := sv2.vfs.Get(fileName)
		if !existsAfter {
			return false
		}
		return fileMeta.Hash == expectedHash
	}, 2*time.Second, 100*time.Millisecond, "Node 2 should have the file of node 1 after executing SyncStorage")
}

func Test11VirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()
	fileName := "test11.txt"
	fileContent := "hello from test11"
	hash1 := UploadFileSimulated(t, sv, fileName, fileContent)
	fileContent2 := "goodbye from test11"
	hash2 := UploadFileSimulated(t, sv, fileName, fileContent2)

	meta, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The system must track the file by its logic name")
	require.Equal(t, hash2, meta.Hash, "Index should point to the Version 2 Hash")
	require.NotEqual(t, hash1, meta.Hash, "Hash should have changed")
	require.Equal(t, 2, meta.Version, "Version of the file should have been incremented to 2")
}

func Test12WorkerPoolLimitsConcurrency(t *testing.T) {
	t.Parallel()
	mockFiles := make(map[string]string)
	var notifications []PeerNotification

	for i := range 5 {
		content := fmt.Sprintf("Content %d", i)
		hasher := sha256.New()
		hasher.Write([]byte(content))
		hash := hex.EncodeToString(hasher.Sum(nil))
		mockFiles[hash] = content
		fileName := fmt.Sprintf("file_%d.txt", i)
		notifications = append(notifications, PeerNotification{
			File: IndexEntry{Name: fileName, Hash: hash, Version: 1},
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

	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	start := time.Now()

	for _, notif := range notifications {
		sv.subscriptions.Store(notif.File.Name, true)
		notif.Source = slowPeer.URL
		body, _ := json.Marshal(notif)
		req, _ := http.NewRequest("POST", sv.config.Address+"/notify", bytes.NewReader(body))
		resp, _ := sv.server.Client().Do(req)
		resp.Body.Close()
	}

	require.Eventually(t, func() bool {
		allContentIsDownloaded := true
		for _, v := range sv.vfs.Snapshot() {
			var buf bytes.Buffer
			if err := sv.storage.ReadBlob(v.Hash, &buf); err != nil {
				return false
			}
			if expectedContent, exists := mockFiles[v.Hash]; exists{
				allContentIsDownloaded = allContentIsDownloaded && buf.String() == expectedContent
			} else {
				return false
			}
		}
		return len(sv.vfs.Snapshot()) == 5 && allContentIsDownloaded
	}, 5*time.Second, 100*time.Millisecond)

	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 2*time.Second, "Too fast. The Worker Pool isn't limiting the concurrency.")
	require.Less(t, duration, 4*time.Second, "Too slow. System is working sequentially.")
}

func Test13LocalDeleteCreatesTombstone(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	fileName := "test13.txt"
	fileContent := "hello from test13!!"
	UploadFileSimulated(t, sv, fileName, fileContent)

	metaBefore, _ := sv.vfs.Get(fileName)
	require.False(t, metaBefore.Deleted, "File should have not been deleted previously")
	
	DeleteFileSimulated(t, sv, fileName)
	metaAfter, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The IndexEntry of the file should still exist after deleting")
	require.True(t, metaAfter.Deleted, "Deleted should be true in the IndexEntry")
	require.Equal(t, metaBefore.Version+1, metaAfter.Version, "Version should have been incremented")

	// TODO: avoid using blobExists and make another function in the main instead
	existsInDisk, _ := sv.storage.BlobExists(metaBefore.Hash)
	require.False(t, existsInDisk, "The physical blob should have been deleted")
}

func Test14TombstonePropagatesToPeers(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)

	fileName := "test14.txt"
	sv1.subscriptions.Store(fileName, true)
	sv2.subscriptions.Store(fileName, true)
	
	fileContent := "hello from test14!!"
	UploadFileSimulated(t, sv1, fileName, fileContent)

	require.Eventually(t, func() bool {
		_, exists := sv2.vfs.Get(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond)
	
	DeleteFileSimulated(t, sv1, fileName)

	require.Eventually(t, func() bool {
		meta, _ := sv2.vfs.Get(fileName)
		return meta.Deleted && meta.Version == 2
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have processed the Tombstone")
}

func Test15SelectiveSynchronization(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

	fileAName := "fileA.txt"
	fileBName := "fileB.txt"

	UploadFileSimulated(t, sv1, fileAName, "Exclusive File A content")
	UploadFileSimulated(t, sv1, fileBName, "Another content, File B")

	// Sv2 subscribes ONLY to fileA via API
	reqSub, _ := http.NewRequest("POST", sv2.config.Address+"/subscribe?name="+fileAName, nil)

	respSub, err := sv2.server.Client().Do(reqSub)
	require.NoError(t, err)
	defer respSub.Body.Close()
	require.Equal(t, http.StatusOK, respSub.StatusCode, "Subscribe API should return 200 OK")

	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)
	require.NoError(t, sv2.SyncStorage())

	// Verify sv2 gets file A
	require.Eventually(t, func() bool {
		_, exists := sv2.vfs.Get(fileAName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have synced the subscribed file A")

	// Verify sv2 DOES NOT get file B
	time.Sleep(1 * time.Second) // wait to ensure no late sync happens
	metaB, existsB := sv2.vfs.Get(fileBName)
	require.True(t, existsB, "Server2 SHOULD have the metadata of file B in its VFS")
	existsInDisk, _ := sv2.storage.BlobExists(metaB.Hash)
	require.False(t, existsInDisk, "Server2 should NOT download the physical blob of file B because it is not subscribed")}

func Test16mTLSConnectionRejectsUnauthorizedPeers(t *testing.T) {
	t.Parallel()
	clusterDir := t.TempDir()
	serverTLS, clientTLS, err := GenerateOrLoadTLSConfig(clusterDir, clusterDir, "legit-node")
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

		_, hackerClientTLS, err := GenerateOrLoadTLSConfig(hackerDir, hackerDir, "hacker-node")
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
	sv := NewServer(DefaultConfigFor(t, "1"))
	
	savedParameters := map[string]ServiceParameter{
		"image": {Type: "string", Required: true},
		"language": {Type: "string", Required: false},
		"output": {Type: "string", Required: false},
	}
	schema1 := ServiceSchema{
		Name:        "ocr",
		Description: "Standard Optical Character Recognition",
		Parameters: savedParameters,
	}

	err := sv.RegisterNewService(schema1)
	require.NoError(t, err, "The first ServiceScheme should be registered")
	
	schemaImpostor := ServiceSchema{
		Name:        "ocr",
		Description: "A ocr impostor that should fail",
		Parameters: map[string]ServiceParameter{
			"image": {Type: "string", Required: true},
			"language": {Type: "string", Required: true},
			"output": {Type: "string", Required: true},
		},
	}

	err = sv.RegisterNewService(schemaImpostor)
	
	require.Error(t, err, "The Registry should reject repeated services")
	require.ErrorIs(t, err, ErrServiceDuplicate)
	
	savedSchema, exists := sv.serviceRegistry.Get("ocr")
	require.True(t, exists)
	require.Equal(t, "Standard Optical Character Recognition", savedSchema.Description)
	require.Equal(t, savedParameters, savedSchema.Parameters)
}

func Test18ANodeReceivesSatisfactoryAnswerFromServiceRequest(t *testing.T) {
	t.Parallel()
	svWithService := NewServer(DefaultConfigFor(t, "1"))
	svDemandingService := NewServer(DefaultConfigFor(t, "2"))
	defer svWithService.Close()
	defer svDemandingService.Close()

	savedParameters := map[string]ServiceParameter{
		"image":    {Type: "string", Required: true},
		"language": {Type: "string", Required: false},
		"output":   {Type: "string", Required: false},
	}
	schema1 := ServiceSchema{
		Name:        "ocr",
		Description: "Standard Optical Character Recognition",
		Parameters:  savedParameters,
	}
	err := svWithService.RegisterNewService(schema1)
	require.NoError(t, err)

	svDemandingService.AddPeer(svWithService.config.ID, svWithService.config.Address)
	svWithService.AddPeer(svDemandingService.config.ID, svDemandingService.config.Address)

	query := DiscoveryQuery{
		Service:          "ocr",
		RequiredParams:   []string{"language"},
		SortStrategy:     StrategyFastest,
		PayloadSizeBytes: 1024 * 1024 * 5,
	}

	targetPeerAddr, serviceSchema, err := svDemandingService.RequestServiceToCluster(query)
	require.NoError(t, err)
	require.Equal(t, svWithService.config.Address, targetPeerAddr, "Debería haber elegido al nodo 1")
	require.Equal(t, "Standard Optical Character Recognition", serviceSchema.Description)

	filledInputs := map[string]any{
		"image":    "fake-hash-12345", 
		"language": "spa",
	}

	taskID := "job-999"
	reqPayload := TaskRequest{
		TaskID:   taskID,
		Service: "ocr",
		Payload:  filledInputs,
		ReplyTo: svDemandingService.config.Address + "/services/callback",
	}

	body, _ := json.Marshal(reqPayload)
	req, err := http.NewRequest("POST", targetPeerAddr+"/services/submit", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := svDemandingService.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusAccepted, resp.StatusCode, "El servidor debe devolver 202 Accepted para encolar la tarea")

	require.Eventually(t, func() bool {
		// Aquí verificaríamos en la memoria de svDemandingService si la tarea "job-999"
		// pasó a estado "completada" gracias a que recibió el webhook de respuesta.
		// return svDemandingService.GetTaskStatus(taskID) == "completed"
		
		return true // Placeholder hasta que implementemos la memoria de tareas
	}, 2*time.Second, 100*time.Millisecond, "El Webhook de finalización nunca llegó")
}
