package pangolin

import (
	"fmt"
)

// PangolinAPIError represents an error returned from the Pangolin API.
// It includes the HTTP status code, endpoint, and detailed error message
// for better error handling and debugging.
type PangolinAPIError struct {
	// StatusCode is the HTTP status code returned by the API
	StatusCode int

	// Endpoint is the API endpoint that was called (e.g., "/org/home/sites")
	Endpoint string

	// Message is the detailed error message from the API response
	Message string

	// Method is the HTTP method used (GET, POST, PUT, DELETE)
	Method string
}

// Error implements the error interface
func (e *PangolinAPIError) Error() string {
	return fmt.Sprintf("Pangolin API error (%d) on %s %s: %s",
		e.StatusCode, e.Method, e.Endpoint, e.Message)
}

// IsNotFound returns true if the error is a 404 Not Found error
func (e *PangolinAPIError) IsNotFound() bool {
	return e.StatusCode == 404
}

// IsConflict returns true if the error is a 409 Conflict error
func (e *PangolinAPIError) IsConflict() bool {
	return e.StatusCode == 409
}

// IsUnauthorized returns true if the error is a 401 Unauthorized error
func (e *PangolinAPIError) IsUnauthorized() bool {
	return e.StatusCode == 401
}

// IsForbidden returns true if the error is a 403 Forbidden error
func (e *PangolinAPIError) IsForbidden() bool {
	return e.StatusCode == 403
}

// IsServerError returns true if the error is a 5xx server error
func (e *PangolinAPIError) IsServerError() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
}

// IsRetryable returns true if the error is potentially retryable
// (server errors, rate limiting, timeouts)
func (e *PangolinAPIError) IsRetryable() bool {
	switch e.StatusCode {
	case 429, 502, 503, 504: // Rate limit, Bad Gateway, Service Unavailable, Gateway Timeout
		return true
	default:
		return e.IsServerError()
	}
}

// IsPangolinAPIError checks if an error is a PangolinAPIError
func IsPangolinAPIError(err error) bool {
	_, ok := err.(*PangolinAPIError)
	return ok
}

// AsPangolinAPIError attempts to cast an error to PangolinAPIError
func AsPangolinAPIError(err error) (*PangolinAPIError, bool) {
	apiErr, ok := err.(*PangolinAPIError)
	return apiErr, ok
}
