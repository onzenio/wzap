package httpapi

import (
	"encoding/json"
	"net/http"
)

// envelope is the success response shape shared by every endpoint.
type envelope struct {
	Data any `json:"data"`
}

// errorEnvelope is the failure response shape shared by every endpoint.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON writes data as a JSON envelope with the given status. No-content
// responses are written without a body.
func JSON(w http.ResponseWriter, status int, data any) {
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data})
}

// Error writes an error envelope with the given status and code, echoing the
// request id when the request carries one.
func Error(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if id := RequestIDFromContext(r.Context()); id != "" {
		w.Header().Set(requestIDHeader, id)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Code: code, Message: message}})
}
