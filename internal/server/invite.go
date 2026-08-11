package server

import (
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

// Consume deletes an invitation secret if present.
func (im *InviteManager) Consume(secret string) {
	im.mu.Lock()
	defer im.mu.Unlock()
	delete(im.pendingInvites, secret)
}

// CheckAndConsume verifies an invitation secret. If valid, it consumes (deletes) it from memory and returns its expiration.
func (im *InviteManager) CheckAndConsume(secret string) (time.Time, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	expiration, exists := im.pendingInvites[secret]
	if !exists || !time.Now().Before(expiration) {
		return time.Time{}, false
	}
	delete(im.pendingInvites, secret)
	return expiration, true
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
