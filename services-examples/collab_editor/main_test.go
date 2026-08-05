package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollabEditorMultiClientSync(t *testing.T) {
	docFile := filepath.Join(os.TempDir(), "proxyma_collab_test-doc.txt")
	_ = os.Remove(docFile)
	defer os.Remove(docFile)

	tmpSocket := filepath.Join(os.TempDir(), fmt.Sprintf("test_collab_%d.sock", time.Now().UnixNano()))
	defer os.Remove(tmpSocket)

	if err := startServerHub(tmpSocket); err != nil {
		t.Fatalf("failed to start test hub: %v", err)
	}

	conn1, err := net.Dial("unix", tmpSocket)
	if err != nil {
		t.Fatalf("client 1 failed to connect: %v", err)
	}
	defer conn1.Close()

	conn2, err := net.Dial("unix", tmpSocket)
	if err != nil {
		t.Fatalf("client 2 failed to connect: %v", err)
	}
	defer conn2.Close()

	r1 := bufio.NewReader(conn1)
	r2 := bufio.NewReader(conn2)

	// Client 1 joins
	join1 := Message{Type: "join", DocID: "test-doc", UserID: "user1", UserName: "User 1"}
	b1, _ := json.Marshal(join1)
	_, _ = conn1.Write(append(b1, '\n'))

	// Read snapshot on Client 1
	line1, err := r1.ReadString('\n')
	if err != nil {
		t.Fatalf("client 1 failed to read snapshot: %v", err)
	}
	var snap1 Message
	if err := json.Unmarshal([]byte(line1), &snap1); err != nil || snap1.Type != "snapshot" {
		t.Fatalf("expected snapshot on client 1, got: %s", line1)
	}

	// Client 2 joins
	join2 := Message{Type: "join", DocID: "test-doc", UserID: "user2", UserName: "User 2"}
	b2, _ := json.Marshal(join2)
	_, _ = conn2.Write(append(b2, '\n'))

	// Client 2 gets snapshot
	line2, err := r2.ReadString('\n')
	if err != nil {
		t.Fatalf("client 2 failed to read snapshot: %v", err)
	}
	var snap2 Message
	if err := json.Unmarshal([]byte(line2), &snap2); err != nil || snap2.Type != "snapshot" {
		t.Fatalf("expected snapshot on client 2, got: %s", line2)
	}

	// Client 1 gets user_joined notification from client 2
	userJoinedLine, err := r1.ReadString('\n')
	if err != nil {
		t.Fatalf("client 1 failed to read user_joined: %v", err)
	}
	var ujMsg Message
	if err := json.Unmarshal([]byte(userJoinedLine), &ujMsg); err != nil || ujMsg.Type != "user_joined" {
		t.Fatalf("expected user_joined on client 1, got: %s", userJoinedLine)
	}

	// Client 1 inserts "Hello World"
	ins := Message{Type: "insert", DocID: "test-doc", UserID: "user1", Pos: 0, Text: "Hello World"}
	bIns, _ := json.Marshal(ins)
	_, _ = conn1.Write(append(bIns, '\n'))

	// Both clients receive op broadcast
	opLine1, _ := r1.ReadString('\n')
	var op1 Message
	_ = json.Unmarshal([]byte(opLine1), &op1)
	if op1.Type != "op" || op1.Content != "Hello World" {
		t.Errorf("client 1 expected content 'Hello World', got: %s", opLine1)
	}

	opLine2, _ := r2.ReadString('\n')
	var op2 Message
	_ = json.Unmarshal([]byte(opLine2), &op2)
	if op2.Type != "op" || op2.Content != "Hello World" {
		t.Errorf("client 2 expected content 'Hello World', got: %s", op2.Content)
	}
}
