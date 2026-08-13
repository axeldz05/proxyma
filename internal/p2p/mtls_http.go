package p2p

import (
	"net/http"

	"proxyma/internal/utils"
)

const missingMTLSMessage = "mTLS certificate required"

// ForbidMissingMTLS writes the canonical response for an HTTP request without
// an authenticated peer certificate.
func ForbidMissingMTLS(w http.ResponseWriter) {
	utils.RespondError(w, http.StatusForbidden, missingMTLSMessage)
}

// RequirePeerCN extracts the authenticated peer identity or writes the
// canonical missing-mTLS response.
func RequirePeerCN(w http.ResponseWriter, r *http.Request) (cn string, ok bool) {
	cn, ok = PeerCNFromTLS(r.TLS)
	if !ok {
		ForbidMissingMTLS(w)
	}
	return
}
