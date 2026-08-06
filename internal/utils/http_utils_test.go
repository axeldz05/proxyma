package utils_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"proxyma/internal/utils"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeAndRespondJSON(t *testing.T) {
	t.Parallel()
	body := bytes.NewBufferString(`{"name":"peer-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/", body)
	got, err := utils.DecodeJSON[map[string]string](req)
	require.NoError(t, err)
	require.Equal(t, "peer-a", got["name"])

	rec := httptest.NewRecorder()
	utils.RespondJSON(rec, http.StatusOK, map[string]string{"ok": "true"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var payload map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&payload))
	require.Equal(t, "true", payload["ok"])
}

func TestRespondErrorAndDecodeJSONOrError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	utils.RespondError(rec, http.StatusBadRequest, "boom")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	badReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{bad"))
	badRec := httptest.NewRecorder()
	_, ok := utils.DecodeJSONOrError[map[string]any](badRec, badReq)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, badRec.Code)

	goodReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"x":1}`))
	goodRec := httptest.NewRecorder()
	payload, ok := utils.DecodeJSONOrError[map[string]any](goodRec, goodReq)
	require.True(t, ok)
	require.Equal(t, float64(1), payload["x"])
}

func TestGetRequiredQueryParam(t *testing.T) {
	t.Parallel()
	miss := httptest.NewRequest(http.MethodGet, "/x", nil)
	missRec := httptest.NewRecorder()
	_, ok := utils.GetRequiredQueryParam(missRec, miss, "id")
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, missRec.Code)

	okReq := httptest.NewRequest(http.MethodGet, "/x?id=node-1", nil)
	okRec := httptest.NewRecorder()
	val, ok := utils.GetRequiredQueryParam(okRec, okReq, "id")
	require.True(t, ok)
	require.Equal(t, "node-1", val)
}
