package main

import (
    "fmt"
    "net/http"
    "net/http/httptest"
	"pcc/storage"
    "os"
    "time"
	"testing"
)

func NewServer(id, storagePath string) *Server {
    s := &Server{
        ID:         id,
        Client:     &http.Client{Timeout: 10 * time.Second},
        Peers:      make(map[string]string),
        storageDir: storagePath,
        files:      make(map[string]FileInfo),
    }
    
    // Create storage directory if it doesn't exist
    os.MkdirAll(storagePath, 0755)
    
    // Set up HTTP handlers
    mux := http.NewServeMux()
    mux.HandleFunc("/upload", s.handleUpload)
    mux.HandleFunc("/notify", s.handleNotification)
    mux.HandleFunc("/download/", s.handleDownload)
    mux.HandleFunc("/peers", s.handlePeers)
    
    // Create test server (in production, you'd use a real server)
    s.server = httptest.NewServer(mux)
    s.Address = s.server.URL
    
    return s
}

func Test01FirstServerIsAlreadySynced(t *testing.T){
	// cuando busca otros dispositivos conectados
	// deberia de encontrar que no hay y de ahi dar la señal de que ya esta
	// sincronizado
	sv := httptest.NewServer(http.HandlerFunc(GetUpdateHandler))
	defer sv.Close()
	aStorage := storage.NewStorage(t.TempDir())
    c := PccClient{sv.Client(), aStorage}
    res, err := c.SyncStorage()
    if err != nil {
        t.Errorf("expected err to be nil got %v", err)
    }
	if !res{
		t.Fatalf("should return true")
	}
}

func Test02AllServersSyncsToLastUpdated(t *testing.T){
	// cuando busca otros dispositivos conectados
	// deberia de encontrar que no hay y de ahi dar la señal de que ya esta
	// sincronizado
	updatedServer := httptest.NewServer(http.HandlerFunc(GetUpdateHandler))
	noUpdatedServer := httptest.NewServer(http.HandlerFunc(GetUpdateHandler))
	noUpdatedServer2 := httptest.NewServer(http.HandlerFunc(GetUpdateHandler))
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()

	aStorage := storage.NewStorage(t.TempDir())
	aStorage1 := storage.NewStorage(t.TempDir())
	aStorage2 := storage.NewStorage(t.TempDir())
    c := PccClient{updatedServer.Client(), aStorage}
    c2 := PccClient{noUpdatedServer.Client(), aStorage1}
    c3 := PccClient{noUpdatedServer2.Client(), aStorage2}
	c.UploadFile("aFile", []byte("wow!"))
    res, err := c.SyncStorage()
    if err != nil {
        t.Errorf("expected err to be nil got %v", err)
    }
	if !res{
		t.Fatalf("should return true")
	}
}

func Test03WhenServerStartsItSyncsWithLatestUpdatedDevice(t *testing.T){
	// deberia de buscar otros dispositivos conectados
	// acceder a /api/sync o similar del primer dispositivo conectado
	// si la request que devuelve es un "actualizado", comienza la descarga.
	// de lo contrario, busca otro dispositivo o devuelve error
	// se testearia con dos servidores; uno actualizado, y otro por actualizarse
	httptest.NewServer(http.HandlerFunc(GetUpdateHandler))
	req := httptest.NewRequest("GET", "/api/getUpdate", nil)
    rr := httptest.NewRecorder()
    handler := http.HandlerFunc(HealthCheckHandler)

    handler.ServeHTTP(rr, req)

    if status := rr.Code; status != http.StatusOK {
        t.Errorf("Expected status 200, got %d", status)
    }

    expected := "OK"
    if rr.Body.String() != expected {
        t.Errorf("Expected body '%s', got '%s'", expected, rr.Body.String())
    }
}



func main() {
    // Create temporary directories for our servers
    dir1, _ := os.MkdirTemp("", "server1")
    dir2, _ := os.MkdirTemp("", "server2")
    dir3, _ := os.MkdirTemp("", "server3")
    defer os.RemoveAll(dir1)
    defer os.RemoveAll(dir2)
    defer os.RemoveAll(dir3)
    
    // Create three servers
    server1 := NewServer("server1", dir1)
    server2 := NewServer("server2", dir2)
    server3 := NewServer("server3", dir3)
    defer server1.Close()
    defer server2.Close()
    defer server3.Close()
    
    // Set up peer relationships
    server1.AddPeer("server2", server2.Address)
    server1.AddPeer("server3", server3.Address)
    
    server2.AddPeer("server1", server1.Address)
    server2.AddPeer("server3", server3.Address)
    
    server3.AddPeer("server1", server1.Address)
    server3.AddPeer("server2", server2.Address)
    
    fmt.Printf("Server 1: %s\n", server1.Address)
    fmt.Printf("Server 2: %s\n", server2.Address)
    fmt.Printf("Server 3: %s\n", server3.Address)
    
    // Simulate a client uploading a file to server1
    fmt.Println("Uploading file to server1...")
    
    // In a real scenario, you would make a multipart form request
    // For this example, we'll simulate it by calling the handler directly
    req := httptest.NewRequest("POST", "/upload", nil)
    // In practice, you'd need to create a proper multipart request here
    w := httptest.NewRecorder()
    
    // This is a simplified example - in reality you'd need to create a proper multipart request
    server1.handleUpload(w, req)
    
    if w.Code == http.StatusCreated {
        fmt.Println("File uploaded successfully to server1")
        fmt.Println("Servers will now synchronize the file among themselves")
    } else {
        fmt.Printf("Upload failed with status: %d\n", w.Code)
    }
    
    // Give some time for synchronization
    time.Sleep(2 * time.Second)
    
    // Check if files are synchronized
    fmt.Println("\nChecking file synchronization:")
    servers := []*Server{server1, server2, server3}
    for i, server := range servers {
        server.mutex.RLock()
        fileCount := len(server.files)
        server.mutex.RUnlock()
        fmt.Printf("Server %d has %d files\n", i+1, fileCount)
    }
    
    fmt.Println("\nPress Ctrl+C to exit...")
    select {} // Block forever
}
