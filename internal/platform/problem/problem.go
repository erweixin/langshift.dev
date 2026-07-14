// Package problem writes the canonical RFC 9457-compatible API error shape.
package problem

import (
	"encoding/json"
	"net/http"
)

type Value struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
	Detail    string `json:"detail,omitempty"`
	Retryable bool   `json:"retryable"`
}

func Write(writer http.ResponseWriter, value Value) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(value.Status)
	_ = json.NewEncoder(writer).Encode(value)
}
