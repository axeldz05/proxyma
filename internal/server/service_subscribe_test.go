package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceSubscriptionReceivesAddAndRemoveNotify(t *testing.T) {
	t.Parallel()

	cfg := testutil.DefaultConfig(t, "svc-sub-node")
	sv := server.New(cfg, nil)

	require.NoError(t, sv.LocalServiceSubscribe("ocr", true))

	postNotify := func(action, nodeID, name, desc string) {
		t.Helper()
		body, _ := json.Marshal(protocol.ServiceNotification{
			Action: action,
			NodeID: nodeID,
			Schema: protocol.ServiceSchema{Name: name, Description: desc},
		})
		req, _ := http.NewRequest(http.MethodPost, "/services/notify", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
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
	sv := server.New(cfg, nil)
	require.NoError(t, sv.LocalServiceSubscribe("vision.*", true))

	body, _ := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-c",
		Schema: protocol.ServiceSchema{Name: "vision.detect", Description: "ok"},
	})
	req, _ := http.NewRequest(http.MethodPost, "/services/notify", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, sv.GetClusterServices("peer-c"), "vision.detect")

	body, _ = json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-c",
		Schema: protocol.ServiceSchema{Name: "audio.transcribe", Description: "no"},
	})
	req, _ = http.NewRequest(http.MethodPost, "/services/notify", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.NotContains(t, sv.GetClusterServices("peer-c"), "audio.transcribe")
}

func TestServiceNotifyWithoutSubscriptionAcceptsAll(t *testing.T) {
	t.Parallel()
	// Backward compat: empty subscription set still materializes (join sync / legacy).
	cfg := testutil.DefaultConfig(t, "svc-sub-legacy")
	sv := server.New(cfg, nil)

	body, _ := json.Marshal(protocol.ServiceNotification{
		Action: protocol.ActionAdd,
		NodeID: "peer-x",
		Schema: protocol.ServiceSchema{Name: "any-svc", Description: "legacy"},
	})
	req, _ := http.NewRequest(http.MethodPost, "/services/notify", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	sv.HandleServiceNotify(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, sv.GetClusterServices("peer-x"), "any-svc")
}