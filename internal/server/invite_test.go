package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func TestInviteCheckAndConsumeIsAtomic(t *testing.T) {
	t.Parallel()
	store := newFakeInviteStore()
	im, err := NewInviteManager(slog.Default(), store)
	require.NoError(t, err)
	secret := "shared-secret"
	require.NoError(t, im.Add(secret, time.Now().Add(time.Hour)))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := im.CheckAndConsume(secret); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
	require.NotContains(t, store.snapshot(), secret)
}

func TestInviteManagerDoesNotPublishFailedWrite(t *testing.T) {
	t.Parallel()
	store := newFakeInviteStore()
	store.saveErr = errors.New("disk full")
	im, err := NewInviteManager(slog.Default(), store)
	require.NoError(t, err)

	require.Error(t, im.Add("secret", time.Now().Add(time.Hour)))
	_, ok := im.Check("secret")
	require.False(t, ok)
}

func TestInviteManagerKeepsInviteWhenDeleteFails(t *testing.T) {
	t.Parallel()
	store := newFakeInviteStore()
	im, err := NewInviteManager(slog.Default(), store)
	require.NoError(t, err)
	require.NoError(t, im.Add("secret", time.Now().Add(time.Hour)))
	store.deleteErr = errors.New("database unavailable")

	_, consumed, err := im.CheckAndConsume("secret")
	require.Error(t, err)
	require.False(t, consumed)
	_, ok := im.Check("secret")
	require.True(t, ok)
}

func TestInviteManagerSweepsExpiredInvitesLoadedFromStore(t *testing.T) {
	t.Parallel()
	store := newFakeInviteStore()
	store.invites["expired"] = time.Now().Add(-time.Minute)
	store.invites["valid"] = time.Now().Add(time.Hour)

	im, err := NewInviteManager(slog.Default(), store)
	require.NoError(t, err)
	_, expired := im.Check("expired")
	_, valid := im.Check("valid")
	require.False(t, expired)
	require.True(t, valid)
	require.NotContains(t, store.snapshot(), "expired")
}

func TestInviteManagerReportsLoadFailure(t *testing.T) {
	t.Parallel()
	store := newFakeInviteStore()
	store.loadErr = errors.New("corrupt bucket")

	im, err := NewInviteManager(slog.Default(), store)
	require.Error(t, err)
	require.Nil(t, im)
}

func TestLocalInviteGenerateDoesNotReturnUnpersistedToken(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "invite-write-failure")
	cfg.Address = "https://127.0.0.1:8443"
	cfg.CAPath = testutil.InitClusterCA(t, cfg.StoragePath)
	store := newFakeInviteStore()
	im, err := NewInviteManager(cfg.Logger, store)
	require.NoError(t, err)
	store.saveErr = errors.New("disk full")
	s := &Server{Config: cfg, Invites: im}

	token, expiration, err := s.LocalInviteGenerate(15)
	require.Error(t, err)
	require.Empty(t, token)
	require.True(t, expiration.IsZero())
}

func TestInvitesSurviveServerRestartAndRemainSingleUse(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "invite-restart")
	expiration := time.Now().UTC().Add(time.Hour)

	first, err := New(cfg, &testutil.MockPeerClient{})
	require.NoError(t, err)
	require.NoError(t, first.Invites.Add("restart-secret", expiration))
	require.NoError(t, first.Shutdown(context.Background()))

	second, err := New(cfg, &testutil.MockPeerClient{})
	require.NoError(t, err)
	loadedExpiration, ok := second.Invites.Check("restart-secret")
	require.True(t, ok)
	require.Equal(t, expiration, loadedExpiration)
	_, consumed, err := second.Invites.CheckAndConsume("restart-secret")
	require.NoError(t, err)
	require.True(t, consumed)
	require.NoError(t, second.Shutdown(context.Background()))

	third, err := New(cfg, &testutil.MockPeerClient{})
	require.NoError(t, err)
	_, ok = third.Invites.Check("restart-secret")
	require.False(t, ok)
	require.NoError(t, third.Shutdown(context.Background()))
}

func TestClusterJoinRestoresInviteDurablyWhenSigningFails(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "invite-restore")
	cfg.CAPath = testutil.InitClusterCA(t, cfg.StoragePath)
	store := newFakeInviteStore()
	im, err := NewInviteManager(cfg.Logger, store)
	require.NoError(t, err)
	expiration := time.Now().Add(time.Hour)
	require.NoError(t, im.Add("restore-secret", expiration))
	require.NoError(t, os.WriteFile(p2p.CAKeyPath(cfg.CAPath), []byte("invalid key"), 0o600))
	csr, _, err := p2p.GenerateNodeCSR("joiner")
	require.NoError(t, err)
	body, err := json.Marshal(protocol.JoinRequest{
		Secret:  "restore-secret",
		CSR:     string(csr),
		ID:      "joiner",
		Address: "https://127.0.0.1:8443",
	})
	require.NoError(t, err)
	s := &Server{Config: cfg, Invites: im}
	req := httptest.NewRequest(http.MethodPost, protocol.PathClusterJoin, bytes.NewReader(body))
	rec := httptest.NewRecorder()

	s.HandleClusterJoin(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	loaded, ok := store.snapshot()["restore-secret"]
	require.True(t, ok)
	require.Equal(t, expiration.UTC(), loaded)
}

type fakeInviteStore struct {
	mu        sync.Mutex
	invites   map[string]time.Time
	saveErr   error
	deleteErr error
	loadErr   error
}

func newFakeInviteStore() *fakeInviteStore {
	return &fakeInviteStore{invites: make(map[string]time.Time)}
}

func (s *fakeInviteStore) SaveInvite(secret string, expiration time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.invites[secret] = expiration
	return nil
}

func (s *fakeInviteStore) DeleteInvite(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.invites, secret)
	return nil
}

func (s *fakeInviteStore) LoadInvites() (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return cloneInvites(s.invites), nil
}

func (s *fakeInviteStore) snapshot() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneInvites(s.invites)
}

func cloneInvites(source map[string]time.Time) map[string]time.Time {
	cloned := make(map[string]time.Time, len(source))
	for secret, expiration := range source {
		cloned[secret] = expiration
	}
	return cloned
}
