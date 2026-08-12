package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

type lifecycleAddr string

func (a lifecycleAddr) Network() string { return string(a) }
func (a lifecycleAddr) String() string  { return string(a) }

type lifecycleListener struct {
	acceptStarted chan struct{}
	closed        chan struct{}
	acceptOnce    sync.Once
	closeOnce     sync.Once
}

func newLifecycleListener() *lifecycleListener {
	return &lifecycleListener{
		acceptStarted: make(chan struct{}),
		closed:        make(chan struct{}),
	}
}

func (l *lifecycleListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.acceptStarted) })
	<-l.closed
	return nil, net.ErrClosed
}

func (l *lifecycleListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *lifecycleListener) Addr() net.Addr { return lifecycleAddr("lifecycle") }

func TestShutdownCancelsPipelineBeforeWaitingForHTTP(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	taskStarted := make(chan struct{})
	taskCanceled := make(chan struct{})
	if err := srv.Compute.RegisterNewService(protocol.ServiceSchema{
		Name:       "blocking-pipeline-step",
		Parameters: map[string]protocol.ServiceParameter{},
	}, func(ctx context.Context, _ <-chan map[string]any, _ chan<- map[string]any, _ map[string]any) (map[string]any, error) {
		close(taskStarted)
		<-ctx.Done()
		close(taskCanceled)
		return nil, ctx.Err()
	}); err != nil {
		t.Fatalf("register blocking service: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		err := srv.Compute.SubmitTask(protocol.TaskRequest{
			TaskID:  "active-http-pipeline",
			Service: "blocking-pipeline-step",
			Payload: map[string]any{},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		<-taskCanceled
		w.WriteHeader(http.StatusNoContent)
	})
	httpTestServer := httptest.NewServer(handler)
	t.Cleanup(httpTestServer.Close)
	srv.httpServer = httpTestServer.Config

	requestDone := make(chan error, 1)
	go func() {
		resp, err := httpTestServer.Client().Get(httpTestServer.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	waitServerLifecycleSignal(t, taskStarted, "HTTP pipeline task start")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		srv.Compute.Close()
		select {
		case <-shutdownDone:
		case <-time.After(serverLifecycleTestTimeout):
			t.Fatal("Shutdown remained deadlocked after compute cleanup")
		}
		t.Fatal("Shutdown waited for HTTP before canceling its active pipeline")
	}

	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatalf("active HTTP request: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("active HTTP request did not exit during shutdown")
	}
}

func TestListenAndServeStartupSerializesWithShutdown(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	unixListener := newLifecycleListener()
	httpListener := newLifecycleListener()
	tcpFactoryEntered := make(chan struct{})
	releaseTCPFactory := make(chan struct{})
	srv.listenFunc = func(network, _ string) (net.Listener, error) {
		switch network {
		case "unix":
			return unixListener, nil
		case "tcp":
			close(tcpFactoryEntered)
			<-releaseTCPFactory
			return httpListener, nil
		default:
			return nil, errors.New("unexpected network")
		}
	}

	serverErr := make(chan error, 1)
	serverTLS := testutil.NewNodeTLS(t, "serialized-start").ServerTLS
	go func() {
		serverErr <- srv.ListenAndServe(serverTLS)
	}()
	waitServerLifecycleSignal(t, tcpFactoryEntered, "TCP listener factory")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned while listener startup was in flight: %v", err)
	default:
	}

	close(releaseTCPFactory)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("Shutdown did not finish after listener startup completed")
	}
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("ListenAndServe error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("ListenAndServe did not exit after shutdown")
	}
}

func TestListenAndServeAfterShutdownDoesNotBind(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	called := make(chan struct{}, 1)
	srv.listenFunc = func(_, _ string) (net.Listener, error) {
		called <- struct{}{}
		return nil, errors.New("must not bind")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	err := srv.ListenAndServe(nil)
	if !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("ListenAndServe after shutdown = %v, want http.ErrServerClosed", err)
	}
	select {
	case <-called:
		t.Fatal("ListenAndServe bound a listener after shutdown")
	default:
	}
}

func TestShutdownClosesListenersBeforeWaitingForDownloadWorkers(t *testing.T) {
	t.Parallel()

	downloadStarted := make(chan struct{})
	downloadCanceled := make(chan struct{})
	releaseDownload := make(chan struct{})
	var releaseOnce sync.Once
	client := &testutil.MockPeerClient{
		OnDownloadBlob: func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			close(downloadStarted)
			<-ctx.Done()
			close(downloadCanceled)
			<-releaseDownload
			return nil, ctx.Err()
		},
	}
	srv := newLifecycleServer(t, client)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseDownload) })
	})

	unixListener := newLifecycleListener()
	httpListener := newLifecycleListener()
	srv.listenFunc = func(network, _ string) (net.Listener, error) {
		if network == "unix" {
			return unixListener, nil
		}
		return httpListener, nil
	}
	serverTLS := testutil.NewNodeTLS(t, "listener-first-shutdown").ServerTLS
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe(serverTLS)
	}()
	waitServerLifecycleSignal(t, unixListener.acceptStarted, "Unix listener accept")
	waitServerLifecycleSignal(t, httpListener.acceptStarted, "HTTP listener accept")

	if err := srv.enqueueDownload(DownloadJob{
		File:   protocol.IndexEntry{Hash: "blocked-download"},
		Source: "peer-a",
	}); err != nil {
		t.Fatalf("enqueue download: %v", err)
	}
	waitServerLifecycleSignal(t, downloadStarted, "download start")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()
	waitServerLifecycleSignal(t, downloadCanceled, "download cancellation")
	waitServerLifecycleSignal(t, unixListener.closed, "Unix listener close")
	waitServerLifecycleSignal(t, httpListener.closed, "HTTP listener close")

	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before the download worker exited: %v", err)
	default:
	}
	releaseOnce.Do(func() { close(releaseDownload) })
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("Shutdown did not finish after download release")
	}
	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("ListenAndServe error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("ListenAndServe did not return after listener close")
	}
}

func TestShutdownDeadlineReturnsWhileFinalizationJoinsWorkers(t *testing.T) {
	t.Parallel()

	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var releaseOnce sync.Once
	client := &testutil.MockPeerClient{
		OnDownloadBlob: func(context.Context, string, string) (io.ReadCloser, error) {
			close(downloadStarted)
			<-releaseDownload
			return nil, context.Canceled
		},
	}
	srv := newLifecycleServer(t, client)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseDownload) })
	})
	if err := srv.enqueueDownload(DownloadJob{
		File:   protocol.IndexEntry{Hash: "deadline-download"},
		Source: "peer-a",
	}); err != nil {
		t.Fatalf("enqueue download: %v", err)
	}
	waitServerLifecycleSignal(t, downloadStarted, "deadline download start")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := srv.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v, want context.Canceled", err)
	}
	select {
	case <-srv.shutdownDone:
		t.Fatal("shutdown finalized while a download worker still owned storage")
	default:
	}

	releaseOnce.Do(func() { close(releaseDownload) })
	waitServerLifecycleSignal(t, srv.shutdownDone, "background shutdown finalization")
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("completed shutdown: %v", err)
	}
}

func TestShutdownJoinsOwnedStorageWorkBeforeClose(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	started := make(chan struct{})
	release := make(chan struct{})
	storageAccessed := make(chan struct{})
	if !srv.goOwned(func() {
		close(started)
		<-release
		_, _ = srv.Storage.GetVFSSnapshot()
		close(storageAccessed)
	}) {
		t.Fatal("server rejected owned work before shutdown")
	}
	waitServerLifecycleSignal(t, started, "owned storage work start")

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(context.Background())
	}()
	waitServerLifecycleSignal(t, srv.lifetimeCtx.Done(), "server lifetime cancellation")
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before owned storage work exited: %v", err)
	default:
	}

	close(release)
	waitServerLifecycleSignal(t, storageAccessed, "owned storage access")
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(serverLifecycleTestTimeout):
		t.Fatal("Shutdown did not join owned storage work")
	}
}
