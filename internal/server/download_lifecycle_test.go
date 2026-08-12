package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

const serverLifecycleTestTimeout = 2 * time.Second

func newLifecycleServer(t *testing.T, client *testutil.MockPeerClient) *Server {
	t.Helper()
	cfg := protocol.NodeConfig{
		ID:          "lifecycle-test",
		StoragePath: t.TempDir(),
		Workers:     1,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv, err := New(cfg, client)
	if err != nil {
		t.Fatalf("create lifecycle server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), serverLifecycleTestTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("cleanup shutdown: %v", err)
		}
	})
	return srv
}

func waitServerLifecycleSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestShutdownCancelsAndJoinsActiveDownloadBeforeStorageClose(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	abort := make(chan struct{})
	downloadReturned := make(chan struct{})
	var releaseOnce, abortOnce sync.Once

	content := "must-not-be-stored-after-cancellation"
	hash := sha256.Sum256([]byte(content))
	hashString := hex.EncodeToString(hash[:])

	client := &testutil.MockPeerClient{
		OnDownloadBlob: func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			defer close(downloadReturned)
			close(started)
			select {
			case <-ctx.Done():
				close(canceled)
			case <-abort:
				return nil, errors.New("test aborted")
			}
			<-release
			return io.NopCloser(strings.NewReader(content)), nil
		},
	}
	srv := newLifecycleServer(t, client)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		abortOnce.Do(func() { close(abort) })
	})

	if err := srv.enqueueDownload(DownloadJob{
		File:   protocol.IndexEntry{Hash: hashString},
		Source: "peer-a",
	}); err != nil {
		t.Fatalf("enqueue download: %v", err)
	}
	waitServerLifecycleSignal(t, started, "download start")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()

	select {
	case <-canceled:
	case err := <-shutdownDone:
		abortOnce.Do(func() { close(abort) })
		waitServerLifecycleSignal(t, downloadReturned, "aborted download return")
		t.Fatalf("Shutdown returned before canceling the active download: %v", err)
	case <-time.After(serverLifecycleTestTimeout):
		abortOnce.Do(func() { close(abort) })
		waitServerLifecycleSignal(t, downloadReturned, "aborted download return")
		t.Fatal("Shutdown did not cancel the active download")
	}

	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before the canceled download exited: %v", err)
	default:
	}

	releaseOnce.Do(func() { close(release) })
	waitServerLifecycleSignal(t, downloadReturned, "download return")
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("Shutdown did not join the download worker")
	}

	if _, err := os.Stat(srv.Storage.GetBlobPath(hashString)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled download mutated storage: stat error = %v", err)
	}
}

func TestEnqueueDownloadRejectsAfterShutdownWinsLifecycleGate(t *testing.T) {
	t.Parallel()

	srv := &Server{
		done:            make(chan struct{}),
		downloadQueue:   make(chan DownloadJob, 1),
		shutdownStarted: true,
	}
	err := srv.enqueueDownload(DownloadJob{
		File:   protocol.IndexEntry{Hash: "late"},
		Source: "peer-a",
	})
	if err == nil {
		t.Fatal("enqueueDownload accepted work after shutdown")
	}
	if got := len(srv.downloadQueue); got != 0 {
		t.Fatalf("download queue length = %d, want 0 after rejected enqueue", got)
	}
}
