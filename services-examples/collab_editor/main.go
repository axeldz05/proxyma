package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const SocketPath = "/tmp/proxyma_collab_hub.sock"

type UserInfo struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Pos      int    `json:"pos"`
}

type Message struct {
	Type     string              `json:"type"` // join, insert, delete, cursor, snapshot, op, user_joined, user_left
	DocID    string              `json:"doc_id,omitempty"`
	UserID   string              `json:"user_id,omitempty"`
	UserName string              `json:"user_name,omitempty"`
	Pos      int                 `json:"pos,omitempty"`
	Len      int                 `json:"len,omitempty"`
	Text     string              `json:"text,omitempty"`
	Action   string              `json:"action,omitempty"`
	Content  string              `json:"content,omitempty"`
	Users    map[string]UserInfo `json:"users,omitempty"`
	Error    string              `json:"error,omitempty"`
}

type Document struct {
	mu          sync.RWMutex
	ID          string
	Content     string
	Users       map[string]UserInfo
	Subscribers map[net.Conn]string // net.Conn -> UserID
}

type Hub struct {
	mu        sync.RWMutex
	documents map[string]*Document
}

var globalHub = &Hub{
	documents: make(map[string]*Document),
}

func (h *Hub) getOrCreateDoc(docID string) *Document {
	h.mu.Lock()
	defer h.mu.Unlock()

	if docID == "" {
		docID = "default-doc"
	}

	doc, exists := h.documents[docID]
	if !exists {
		// Attempt to load existing persistence from disk
		content := ""
		saveFile := filepath.Join(os.TempDir(), fmt.Sprintf("proxyma_collab_%s.txt", docID))
		if data, err := os.ReadFile(saveFile); err == nil {
			content = string(data)
		}

		doc = &Document{
			ID:          docID,
			Content:     content,
			Users:       make(map[string]UserInfo),
			Subscribers: make(map[net.Conn]string),
		}
		h.documents[docID] = doc
	}
	return doc
}

func (doc *Document) saveToDisk() {
	saveFile := filepath.Join(os.TempDir(), fmt.Sprintf("proxyma_collab_%s.txt", doc.ID))
	_ = os.WriteFile(saveFile, []byte(doc.Content), 0644)
}

func (doc *Document) broadcast(msg Message, skipConn net.Conn) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	dataLine := append(data, '\n')

	doc.mu.RLock()
	defer doc.mu.RUnlock()

	for conn := range doc.Subscribers {
		if conn == skipConn {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write(dataLine)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	var currentDoc *Document
	var currentUserID string

	defer func() {
		if currentDoc != nil && currentUserID != "" {
			currentDoc.mu.Lock()
			delete(currentDoc.Subscribers, conn)
			delete(currentDoc.Users, currentUserID)
			currentDoc.mu.Unlock()

			currentDoc.broadcast(Message{
				Type:     "user_left",
				DocID:    currentDoc.ID,
				UserID:   currentUserID,
				Users:    currentDoc.Users,
			}, nil)
		}
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				// connection error
			}
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		if msg.DocID == "" {
			msg.DocID = "default-doc"
		}

		doc := globalHub.getOrCreateDoc(msg.DocID)
		currentDoc = doc

		switch msg.Type {
		case "join":
			if msg.UserID == "" {
				msg.UserID = fmt.Sprintf("user_%d", time.Now().UnixNano()%10000)
			}
			if msg.UserName == "" {
				msg.UserName = msg.UserID
			}
			currentUserID = msg.UserID

			doc.mu.Lock()
			doc.Subscribers[conn] = msg.UserID
			doc.Users[msg.UserID] = UserInfo{
				UserID:   msg.UserID,
				UserName: msg.UserName,
				Pos:      msg.Pos,
			}
			snapshotContent := doc.Content
			activeUsers := make(map[string]UserInfo, len(doc.Users))
			for k, v := range doc.Users {
				activeUsers[k] = v
			}
			doc.mu.Unlock()

			// Send snapshot to joining client
			snapMsg := Message{
				Type:     "snapshot",
				DocID:    doc.ID,
				UserID:   msg.UserID,
				UserName: msg.UserName,
				Content:  snapshotContent,
				Users:    activeUsers,
			}
			b, _ := json.Marshal(snapMsg)
			_, _ = conn.Write(append(b, '\n'))

			// Broadcast user_joined to peers
			doc.broadcast(Message{
				Type:     "user_joined",
				DocID:    doc.ID,
				UserID:   msg.UserID,
				UserName: msg.UserName,
				Users:    activeUsers,
			}, conn)

		case "insert":
			doc.mu.Lock()
			runes := []rune(doc.Content)
			pos := msg.Pos
			if pos < 0 {
				pos = 0
			}
			if pos > len(runes) {
				pos = len(runes)
			}

			// Insert text at pos
			left := string(runes[:pos])
			right := string(runes[pos:])
			doc.Content = left + msg.Text + right

			// Update user position
			if u, ok := doc.Users[msg.UserID]; ok {
				u.Pos = pos + len([]rune(msg.Text))
				doc.Users[msg.UserID] = u
			}
			doc.saveToDisk()
			doc.mu.Unlock()

			// Broadcast op
			opMsg := Message{
				Type:     "op",
				Action:   "insert",
				DocID:    doc.ID,
				UserID:   msg.UserID,
				UserName: msg.UserName,
				Pos:      pos,
				Text:     msg.Text,
				Content:  doc.Content,
			}
			doc.broadcast(opMsg, nil)

		case "delete":
			doc.mu.Lock()
			runes := []rune(doc.Content)
			pos := msg.Pos
			length := msg.Len
			if pos < 0 {
				pos = 0
			}
			if pos > len(runes) {
				pos = len(runes)
			}
			end := pos + length
			if end > len(runes) {
				end = len(runes)
			}

			if pos < end {
				left := string(runes[:pos])
				right := string(runes[end:])
				doc.Content = left + right
			}

			if u, ok := doc.Users[msg.UserID]; ok {
				u.Pos = pos
				doc.Users[msg.UserID] = u
			}
			doc.saveToDisk()
			doc.mu.Unlock()

			// Broadcast op
			opMsg := Message{
				Type:     "op",
				Action:   "delete",
				DocID:    doc.ID,
				UserID:   msg.UserID,
				UserName: msg.UserName,
				Pos:      pos,
				Len:      length,
				Content:  doc.Content,
			}
			doc.broadcast(opMsg, nil)

		case "cursor":
			doc.mu.Lock()
			if u, ok := doc.Users[msg.UserID]; ok {
				u.Pos = msg.Pos
				doc.Users[msg.UserID] = u
			}
			doc.mu.Unlock()

			doc.broadcast(Message{
				Type:   "cursor",
				DocID:  doc.ID,
				UserID: msg.UserID,
				Pos:    msg.Pos,
			}, conn)
		}
	}
}

func startServerHub(socketPath string) error {
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleConn(conn)
		}
	}()
	return nil
}

func main() {
	// If launched with argument "--hub", run only as dedicated server hub
	if len(os.Args) > 1 && os.Args[1] == "--hub" {
		if err := startServerHub(SocketPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start hub: %v\n", err)
			os.Exit(1)
		}
		select {}
	}

	// Try to connect to existing socket hub
	conn, err := net.Dial("unix", SocketPath)
	if err != nil {
		// Hub not running yet, start listener in this process
		if err := startServerHub(SocketPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start server hub: %v\n", err)
			os.Exit(1)
		}
		// Retry connecting
		conn, err = net.Dial("unix", SocketPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to dial socket after starting hub: %v\n", err)
			os.Exit(1)
		}
	}
	defer conn.Close()

	// Bridge Stdin -> Socket
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			_, _ = conn.Write([]byte(line + "\n"))
		}
	}()

	// Bridge Socket -> Stdout
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)
	}
}
