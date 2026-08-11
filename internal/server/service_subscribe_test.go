package server_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func withPeerTLS(req *http.Request, peerID string) *http.Request {
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: peerID},
		}},
	}
	return req
}

func TestServiceSubscriptionReceivesAddAndRemoveNotify(t *testing.T) {
	t.Parallel()

	cfg := testutil.DefaultConfig(t, "svc-sub-node")
	sv, err := server.New(cfg, nil)
	require.NoError(t, err)

	require.NoError(t, sv.LocalServiceSubscribe("ocr", true))

	postNotify := func(action, nodeID, name, desc string) {
		t.Helper()
		body, _ := json.Marshal(protocol.ServiceNotification{
			Action: action,
			NodeID: nodeID,
			Schema: protocol.ServiceSchema{Name: name, Description: desc},
		})
		req, _ := http.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = withPeerTLS(req, nodeID)
		rec := httptest.NewRecorder()
		sv.HandleServiceNotify(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	postNotify(protocol.ActionAdd, "peer-b", "ocr", "ocr-svc")
	services := sv.GetClusterServices("peer-b")
	require.Contains(t, services, "ocr")
	require.Equal(t, "ocr-svc", services["ocr"].Description)

	postNotify(protocol.ActionAdd, "peer-b", "translate", "unwanted")
	services = sv.GetClusterServices("peer-b")
	require.NotContains(t, services, "translate", "non-subscribed service must not materialize")

	postNotify(protocol.ActionRemove, "peer-b", "ocr", "ocr-svc")
	services = sv.GetClusterServices("peer-b")
	require.NotContains(t, services, "ocr")
}

func TestServiceSubscriptionPatternMatch(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "svc-sub-pat")
	sv, err := server.New(cfg, nil)
	require.NoError(t, err)
	require.NoError(t, sv.LocalServiceSubscribe("vision.*", true))

	body, _ := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-c",
		Schema: protocol.ServiceSchema{Name: "vision.detect", Description: "ok"},
	})
	req, _ := http.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
	req = withPeerTLS(req, "peer-c")
	rec := httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, sv.GetClusterServices("peer-c"), "vision.detect")

	body, _ = json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-c",
		Schema: protocol.ServiceSchema{Name: "audio.transcribe", Description: "no"},
	})
	req, _ = http.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
	req = withPeerTLS(req, "peer-c")
	rec = httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.NotContains(t, sv.GetClusterServices("peer-c"), "audio.transcribe")
}

func TestServiceNotifyWithoutSubscriptionAcceptsAll(t *testing.T) {
	t.Parallel()
	// Backward compat: empty subscription set still materializes (join sync / legacy).
	cfg := testutil.DefaultConfig(t, "svc-sub-legacy")
	sv, err := server.New(cfg, nil)
	require.NoError(t, err)

	body, _ := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-x",
		Schema: protocol.ServiceSchema{Name: "any-svc", Description: "legacy"},
	})
	req, _ := http.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
	req = withPeerTLS(req, "peer-x")
	rec := httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, sv.GetClusterServices("peer-x"), "any-svc")
}

func TestServiceNotifyRejectsCNMismatch(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "svc-sub-cn")
	sv, err := server.New(cfg, nil)
	require.NoError(t, err)
	require.NoError(t, sv.LocalServiceSubscribe("ocr", true))

	body, _ := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "victim",
		Schema: protocol.ServiceSchema{Name: "ocr", Description: "spoof"},
	})
	req, _ := http.NewRequest(http.MethodPost, protocol.PathServicesNotify, bytes.NewReader(body))
	req = withPeerTLS(req, "attacker")
	rec := httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, sv.GetClusterServices("victim"), "ocr")
}
