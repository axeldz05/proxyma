package server

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type inviteStore interface {
	SaveInvite(secret string, expiration time.Time) error
	DeleteInvite(secret string) error
	LoadInvites() (map[string]time.Time, error)
}

// InviteManager manages invitation secrets, their expirations, and sweeps them periodically.
type InviteManager struct {
	logger         *slog.Logger
	store          inviteStore
	pendingInvites map[string]time.Time
	mu             sync.Mutex
}

// NewInviteManager creates a new InviteManager.
func NewInviteManager(logger *slog.Logger, store inviteStore) (*InviteManager, error) {
	if store == nil {
		return nil, errors.New("invite store is required")
	}
	pending, err := store.LoadInvites()
	if err != nil {
		return nil, fmt.Errorf("load pending invites: %w", err)
	}
	im := &InviteManager{
		logger:         logger,
		store:          store,
		pendingInvites: pending,
	}
	if err := im.Sweep(); err != nil {
		return nil, fmt.Errorf("sweep pending invites: %w", err)
	}
	return im, nil
}

// Add registers an invitation secret with an expiration.
func (im *InviteManager) Add(secret string, expiration time.Time) error {
	im.mu.Lock()
	defer im.mu.Unlock()
	expiration = expiration.UTC()
	if err := im.store.SaveInvite(secret, expiration); err != nil {
		return fmt.Errorf("save invite: %w", err)
	}
	im.pendingInvites[secret] = expiration
	return nil
}

// Check peeks a valid non-expired invitation without deleting it.
func (im *InviteManager) Check(secret string) (time.Time, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	expiration, exists := im.pendingInvites[secret]
	if !exists || !time.Now().Before(expiration) {
		return time.Time{}, false
	}
	return expiration, true
}

// CheckAndConsume verifies an invitation secret. If valid, it durably consumes
// it before deleting the in-memory copy.
func (im *InviteManager) CheckAndConsume(secret string) (time.Time, bool, error) {
	im.mu.Lock()
	defer im.mu.Unlock()
	expiration, exists := im.pendingInvites[secret]
	if !exists {
		return time.Time{}, false, nil
	}
	if !time.Now().Before(expiration) {
		if err := im.store.DeleteInvite(secret); err != nil {
			return time.Time{}, false, fmt.Errorf("delete expired invite: %w", err)
		}
		delete(im.pendingInvites, secret)
		return time.Time{}, false, nil
	}
	if err := im.store.DeleteInvite(secret); err != nil {
		return time.Time{}, false, fmt.Errorf("consume invite: %w", err)
	}
	delete(im.pendingInvites, secret)
	return expiration, true, nil
}

// Sweep removes expired invitations.
func (im *InviteManager) Sweep() error {
	im.mu.Lock()
	defer im.mu.Unlock()
	now := time.Now()
	var sweepErr error
	for secret, expiration := range im.pendingInvites {
		if now.Before(expiration) {
			continue
		}
		if err := im.store.DeleteInvite(secret); err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("delete expired invite: %w", err))
			continue
		}
		delete(im.pendingInvites, secret)
		im.logger.Debug("Expired invite removed from durable store")
	}
	return sweepErr
}
