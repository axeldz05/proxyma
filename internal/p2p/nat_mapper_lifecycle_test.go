package p2p

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fd/go-nat"
)

func TestNATMapperStopJoinsRunningMapper(t *testing.T) {
	t.Parallel()

	nm := NewNATMapper(slog.New(slog.NewTextHandler(io.Discard, nil)), 9000, 9001)
	discoveryStarted := make(chan struct{})
	releaseDiscovery := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseDiscovery) })
		nm.Stop()
	})
	nm.SetGatewayDiscovery(func() (nat.NAT, error) {
		close(discoveryStarted)
		<-releaseDiscovery
		return nil, errors.New("discovery stopped")
	})

	nm.Start()
	select {
	case <-discoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("mapper discovery did not start")
	}

	stopDone := make(chan struct{})
	go func() {
		nm.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the mapper goroutine exited")
	case <-time.After(20 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseDiscovery) })
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not join the mapper goroutine")
	}
}
