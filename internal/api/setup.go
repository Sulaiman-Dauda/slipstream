package api

import (
	"time"

	"github.com/slipstream-panel/slipstream/internal/state"
)

// NewSetupToken mints and stores the one-time first-boot setup token that
// the installer (or panel-api on first start) prints as a setup URL.
func NewSetupToken(store *state.Store, ttl time.Duration) string {
	token := randomToken(24)
	store.CreateSetupToken(token, ttl)
	return token
}
