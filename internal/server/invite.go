package server

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// InviteManager manages invitation secrets, their expirations, and sweeps them periodically.
type InviteManager struct {
	logger         *slog.Logger
	pendingInvites map[string]time.Time
	mu             sync.Mutex
}

// NewInviteManager creates a new InviteManager.
func NewInviteManager(logger *slog.Logger) *InviteManager {
	return &InviteManager{
		logger:         logger,
		pendingInvites: make(map[string]time.Time),
	}
}

// Add registers an invitation secret with an expiration.
func (im *InviteManager) Add(secret string, expiration time.Time) {
	im.mu.Lock()
	defer im.mu.Unlock()
	im.pendingInvites[secret] = expiration
}

// CheckAndConsume verifies an invitation. If it exists, it consumes (deletes) it from memory and returns its expiration.
func (im *InviteManager) CheckAndConsume(secret string) (time.Time, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	expiration, exists := im.pendingInvites[secret]
	if exists {
		delete(im.pendingInvites, secret)
	}
	return expiration, exists
}

// Sweep removes expired invitations.
func (im *InviteManager) Sweep() {
	im.mu.Lock()
	defer im.mu.Unlock()
	now := time.Now()
	for secret, expiration := range im.pendingInvites {
		if now.After(expiration) {
			delete(im.pendingInvites, secret)
			im.logger.Debug("Expired invite removed from memory")
		}
	}
}

// StartSweeper starts a background loop that periodically sweeps expired invitations.
func (im *InviteManager) StartSweeper(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			im.Sweep()
		}
	}
}
