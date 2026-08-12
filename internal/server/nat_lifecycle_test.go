package server

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type shutdownBoundaryWriter struct {
	mu               sync.Mutex
	shutdownReturned bool
	lateWrites       int
}

func (w *shutdownBoundaryWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shutdownReturned {
		w.lateWrites++
	}
	return len(p), nil
}

func (w *shutdownBoundaryWriter) markShutdownReturned() {
	w.mu.Lock()
	w.shutdownReturned = true
	w.mu.Unlock()
}

func (w *shutdownBoundaryWriter) lateWriteCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lateWrites
}

func TestNATLifecycleStressCancelsAndJoinsBeforeLoggerShutdown(t *testing.T) {
	t.Parallel()

	const iterations = 32
	for range iterations {
		srv := newLifecycleServer(t, nil)
		writer := &shutdownBoundaryWriter{}
		srv.Config.Logger = slog.New(slog.NewTextHandler(writer, nil))

		started := make(chan struct{})
		canceled := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(release) })
		})

		srv.natCheck = func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			srv.Config.Logger.Info("NAT check stopped")
			close(finished)
		}
		srv.scheduleNATCheck(0)
		waitServerLifecycleSignal(t, started, "NAT check start")

		shutdownDone := make(chan error, 1)
		go func() {
			shutdownDone <- srv.Shutdown(context.Background())
		}()
		waitServerLifecycleSignal(t, canceled, "NAT check cancellation")

		select {
		case err := <-shutdownDone:
			writer.markShutdownReturned()
			releaseOnce.Do(func() { close(release) })
			waitServerLifecycleSignal(t, finished, "late NAT check finish")
			t.Fatalf("Shutdown returned before the NAT check exited: %v", err)
		default:
		}

		releaseOnce.Do(func() { close(release) })
		waitServerLifecycleSignal(t, finished, "NAT check finish")
		select {
		case err := <-shutdownDone:
			if err != nil {
				t.Fatalf("shutdown: %v", err)
			}
		case <-time.After(serverLifecycleTestTimeout):
			t.Fatal("Shutdown did not finish after joining the NAT check")
		}
		writer.markShutdownReturned()
		if late := writer.lateWriteCount(); late != 0 {
			t.Fatalf("NAT lifecycle logged %d times after Shutdown returned", late)
		}

		if err := srv.Shutdown(context.Background()); err != nil {
			t.Fatalf("idempotent shutdown: %v", err)
		}
	}
}

func TestShutdownCancelsAndJoinsDelayedNATCheck(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, nil)
	started := make(chan struct{}, 1)
	srv.natCheck = func(context.Context) {
		started <- struct{}{}
	}

	scheduledDone := srv.scheduleNATCheck(time.Hour)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case <-scheduledDone:
	default:
		t.Fatal("Shutdown returned before the delayed NAT owner exited")
	}
	select {
	case <-started:
		t.Fatal("delayed NAT check started during shutdown")
	default:
	}
}

func TestCurrentNATStateConcurrentPublication(t *testing.T) {
	t.Parallel()

	srv := newLifecycleServer(t, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := range 500 {
			srv.applySponsorStatus(i%2 == 0)
			srv.refreshPublicUDPFromMapping("198.51.100.7", 10000+i)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 500 {
			state := srv.CurrentNATState()
			_ = state.IsSponsor
			_ = state.PublicUDPAddr
			_ = state.QUICManager
		}
	}()
	close(start)
	wg.Wait()
}
