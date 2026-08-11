package server

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInviteCheckAndConsumeIsAtomic(t *testing.T) {
	t.Parallel()
	im := NewInviteManager(slog.Default())
	secret := "shared-secret"
	im.Add(secret, time.Now().Add(time.Hour))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := im.CheckAndConsume(secret); ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
}
