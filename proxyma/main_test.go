package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"proxyma/storage"
	"strings"
	"testing"
	"time"
	"crypto/sha256"
	"encoding/hex"

	"github.com/stretchr/testify/require"
)

func NewServer(id, storagePath string) *Server {
    s := &Server{
        ID:         id,
//        Client:     &http.Client{Timeout: 10 * time.Second},
        Peers:      make(map[string]string),
        storage: 	*storage.NewStorage(storagePath),
        files:      make(map[string]FileInfo),
    }
    
    os.MkdirAll(storagePath, 0755)
    
    mux := http.NewServeMux()
    mux.HandleFunc("/upload", s.handleUpload)
    mux.HandleFunc("/notify", s.handleNotification)
    mux.HandleFunc("/download/", s.handleDownload)
    mux.HandleFunc("/peers", s.GetPeers)
    
    s.server = httptest.NewServer(mux)
    s.Address = s.server.URL
	s.Client = s.server.Client()
    
    return s
}

func AnAcceptedFileForUpload(t *testing.T) (bytes.Buffer, *multipart.Writer, string){
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	fileContent := "Hello!"
	_, err = io.WriteString(fileWriter, fileContent)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	return requestBody, writer, fileContent
}

func Test01FirstServerIsAlreadySynced(t *testing.T){
	sv := NewServer("1", t.TempDir())
	defer sv.Close()
    res := sv.SyncStorage()
	require.True(t, res, "should return true")
}

func Test02AServerCanConnectToAnother(t *testing.T){
	sv1 := NewServer("1", t.TempDir())
	sv2 := NewServer("2", t.TempDir())
	defer sv1.Close()
	defer sv2.Close()
	sv1.AddPeer("2", sv2.Address)
	sv2.AddPeer("1", sv1.Address)

	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()
	sv2.GetPeers(w, req)	
	resp := w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv2 := strings.TrimSpace(buf.String())
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.Address)

	req = httptest.NewRequest("GET", "/peers", nil)
	w = httptest.NewRecorder()
	sv1.GetPeers(w, req)	
	resp = w.Result()
	buf = new(strings.Builder)
	_, err = io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv1 := strings.TrimSpace(buf.String())
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.Address)
	require.Equal(t,expectedPeersOfSv2,gotPeersOfSv2)
	require.Equal(t,expectedPeersOfSv1,gotPeersOfSv1)
	require.True(t, sv1.SyncStorage(), "sv1 should be synced to sv2")
	require.True(t, sv2.SyncStorage(), "sv2 should be synced to sv1")
}

func Test03AllServersSyncsToLastUpdated(t *testing.T){
	updatedServer := NewServer("1", t.TempDir())
	noUpdatedServer := NewServer("2", t.TempDir())
	noUpdatedServer2 := NewServer("3", t.TempDir())
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()
	
	updatedServer.AddPeer("2", noUpdatedServer.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.Address)

	requestBody, writer, fileContent := AnAcceptedFileForUpload(t)

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))
    
    req := httptest.NewRequest("POST", "/upload", &requestBody)
    req.Header.Set("Content-Type", writer.FormDataContentType())

    w := httptest.NewRecorder()
    updatedServer.handleUpload(w, req)
    
    resp := w.Result()
    if w.Code != http.StatusCreated {
        t.Errorf("Expected status Created, got %v", w.Code)
    }
	_, exists := updatedServer.files[expectedHash]
    if !exists {
        t.Errorf("File hash '%s' was not registered in the metadata", expectedHash)
    }

	downloadURL := fmt.Sprintf("/download/%s", expectedHash)
	req = httptest.NewRequest("GET", downloadURL, nil)
	w = httptest.NewRecorder()
	updatedServer.handleDownload(w, req)
	resp = w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy fileContent from %s", resp.Body)
	}
	uploadedContent := buf.String()
    
    if uploadedContent != fileContent {
        t.Errorf("Expected content %s, got %s", fileContent, string(uploadedContent))
    }
	require.Eventually(t, func() bool {
        noUpdatedServer.mutex.RLock()
        _, exists := noUpdatedServer.files[expectedHash]
        noUpdatedServer.mutex.RUnlock()
        return exists
    }, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	sv := NewServer("1", t.TempDir())
	defer sv.Close()

	requestBody, writer, fileContent := AnAcceptedFileForUpload(t)
    
	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	req := httptest.NewRequest("POST", "/upload", &requestBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	w := httptest.NewRecorder()
	sv.handleUpload(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	sv.mutex.RLock()
	fileMeta, exists := sv.files[expectedHash]
	sv.mutex.RUnlock()

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func Test05P2PNetworkEventualConsistency(t *testing.T) {
	clusterSize := 3
	servers := make([]*Server, clusterSize)
	for i := 0; i < clusterSize; i++ {
		servers[i] = NewServer(fmt.Sprintf("node-%d", i), t.TempDir())
		defer servers[i].Close()
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.ID, peer.Address)
			}
		}
	}

	requestBody, writer, expectedContent := AnAcceptedFileForUpload(t)
	req := httptest.NewRequest("POST", "/upload", &requestBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	
	w := httptest.NewRecorder()
	servers[0].handleUpload(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	hasher := sha256.New()
	hasher.Write([]byte(expectedContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	require.Eventually(t, func() bool {
		for _, srv := range servers {
			srv.mutex.RLock()
			meta, exists := srv.files[expectedHash]
			srv.mutex.RUnlock()
			
			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func Test06DownloadEndpointUsesHashInsteadOfName(t *testing.T) {
	sv := NewServer("1", t.TempDir())
	defer sv.Close()
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t)
	req := httptest.NewRequest("POST", "/upload", &requestBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	
	w := httptest.NewRecorder()
	sv.handleUpload(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	downloadURL := fmt.Sprintf("/download/%s", expectedHash)
	reqDL := httptest.NewRequest("GET", downloadURL, nil)
	wDL := httptest.NewRecorder()
	
	sv.handleDownload(wDL, reqDL)
	
	require.Equal(t, http.StatusOK, wDL.Code, "Server should answer with OK 200 status when requesting Hash")
	require.Equal(t, fileContent, wDL.Body.String(), "Downloaded content should be the same as the uploaded content")
}
