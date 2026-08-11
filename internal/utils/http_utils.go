package utils

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func DecodeJSON[T any](r *http.Request) (T, error) {
	var payload T
	err := json.NewDecoder(r.Body).Decode(&payload)
	return payload, err
}

func RespondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			// Status and headers are already on the wire, so the client just sees a
			// truncated body. Panicking here would take the whole node down.
			slog.Error("Failed to encode JSON response", "status", status, "error", err)
		}
	}
}

func RespondError(w http.ResponseWriter, status int, message string) {
	RespondJSON(w, status, map[string]string{"error": message})
}

// DecodeJSONOrError decodes the request body and returns false with a Bad Request response if decoding fails.
func DecodeJSONOrError[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	payload, err := DecodeJSON[T](r)
	if err != nil {
		RespondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return payload, false
	}
	return payload, true
}

// GetRequiredQueryParam retrieves a query parameter and returns false with a Bad Request response if empty.
func GetRequiredQueryParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	val := r.URL.Query().Get(name)
	if val == "" {
		RespondError(w, http.StatusBadRequest, "Missing '" + name + "' query parameter")
		return "", false
	}
	return val, true
}
