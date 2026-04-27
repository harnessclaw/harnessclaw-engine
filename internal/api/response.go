// Package api implements the Console management HTTP API.
// It is independent from the Channel layer which handles conversation routing.
package api

import (
	"encoding/json"
	"net/http"
)

// Response is the standard API response envelope.
type Response struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Total   *int   `json:"total,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp *Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func writeSuccess(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, &Response{Code: "OK", Data: data})
}

func writeList(w http.ResponseWriter, data any, total int) {
	writeJSON(w, http.StatusOK, &Response{Code: "OK", Data: data, Total: &total})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, &Response{Code: code, Message: message})
}
