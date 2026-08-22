package apiresponse

import (
	"encoding/json"
	"net/http"
)

type ErrorEnvelope struct {
	OK        bool   `json:"ok"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, status int, err error) {
	message := http.StatusText(status)
	if err != nil && err.Error() != "" {
		message = err.Error()
	}
	JSON(w, status, ErrorEnvelope{
		OK:        false,
		Code:      statusCode(status),
		Message:   message,
		Error:     message,
		Retryable: status == http.StatusTooManyRequests || status >= http.StatusInternalServerError,
	})
}

func statusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway:
		return "upstream_unavailable"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_error"
		}
		return "request_failed"
	}
}
