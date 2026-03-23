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
    
    req := httptest.NewRequest("POST", "/upload", &requestBody)
    req.Header.Set("Content-Type", writer.FormDataContentType())

    w := httptest.NewRecorder()
    updatedServer.handleUpload(w, req)
    
    resp := w.Result()
    if resp.StatusCode != http.StatusCreated {
        t.Errorf("Expected status Created, got %v", resp.StatusCode)
    }
	_, exists := updatedServer.files["test.txt"]
    if !exists {
        t.Errorf("File 'test.txt' was registered in the metadata for storage %s", updatedServer.ID)
    }

	req = httptest.NewRequest("GET", "/download/test.txt",nil)
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
        _, exists := noUpdatedServer.files["test.txt"]
        noUpdatedServer.mutex.RUnlock()
        return exists
    }, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	sv := NewServer("1", t.TempDir())
	defer sv.Close()

	requestBody, writer, fileContent := AnAcceptedFileForUpload(t)
    
	req := httptest.NewRequest("POST", "/upload", &requestBody)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	w := httptest.NewRecorder()
	sv.handleUpload(w, req)
    
	require.Equal(t, http.StatusCreated, w.Code)

	sv.mutex.RLock()
	fileMeta, exists := sv.files["test.txt"]
	sv.mutex.RUnlock()

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
    
	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func testServers() {
    dir1, _ := os.MkdirTemp("", "server1")
    dir2, _ := os.MkdirTemp("", "server2")
    dir3, _ := os.MkdirTemp("", "server3")
    defer os.RemoveAll(dir1)
    defer os.RemoveAll(dir2)
    defer os.RemoveAll(dir3)
    
    server1 := NewServer("server1", dir1)
    server2 := NewServer("server2", dir2)
    server3 := NewServer("server3", dir3)
    defer server1.Close()
    defer server2.Close()
    defer server3.Close()
    
    server1.AddPeer("server2", server2.Address)
    server1.AddPeer("server3", server3.Address)
    
    server2.AddPeer("server1", server1.Address)
    server2.AddPeer("server3", server3.Address)
    
    server3.AddPeer("server1", server1.Address)
    server3.AddPeer("server2", server2.Address)
    
    fmt.Printf("Server 1: %s\n", server1.Address)
    fmt.Printf("Server 2: %s\n", server2.Address)
    fmt.Printf("Server 3: %s\n", server3.Address)
    
    fmt.Println("Uploading file to server1...")
    
    req := httptest.NewRequest("POST", "/upload", nil)
    w := httptest.NewRecorder()
    
    server1.handleUpload(w, req)
    
    if w.Code == http.StatusCreated {
        fmt.Println("File uploaded successfully to server1")
        fmt.Println("Servers will now synchronize the file among themselves")
    } else {
        fmt.Printf("Upload failed with status: %d\n", w.Code)
    }
    
    time.Sleep(2 * time.Second)
    
    fmt.Println("\nChecking file synchronization:")
    servers := []*Server{server1, server2, server3}
    for i, server := range servers {
        server.mutex.RLock()
        fileCount := len(server.files)
        server.mutex.RUnlock()
        fmt.Printf("Server %d has %d files\n", i+1, fileCount)
    }
    
    fmt.Println("\nPress Ctrl+C to exit...")
    select {}
}
