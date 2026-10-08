package pangolin

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAPIError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *APIError
		expected string
	}{
		{
			name: "standard error message",
			err: &APIError{
				StatusCode: 404,
				Method:     "GET",
				Endpoint:   "/org/home/sites",
				Message:    "site not found",
			},
			expected: "Pangolin API error (404) on GET /org/home/sites: site not found",
		},
		{
			name: "server error message",
			err: &APIError{
				StatusCode: 500,
				Method:     "PUT",
				Endpoint:   "/org/home/site",
				Message:    "internal server error",
			},
			expected: "Pangolin API error (500) on PUT /org/home/site: internal server error",
		},
		{
			name: "empty fields",
			err: &APIError{
				StatusCode: 0,
				Method:     "",
				Endpoint:   "",
				Message:    "",
			},
			expected: "Pangolin API error (0) on  : ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestAPIError_ImplementsErrorInterface(t *testing.T) {
	var err error = &APIError{
		StatusCode: 400,
		Method:     "GET",
		Endpoint:   "/test",
		Message:    "bad request",
	}
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestAPIError_IsRetryable(t *testing.T) {
	tests := []struct {
		statusCode int
		retryable  bool
	}{
		{200, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{409, false},
		{429, true}, // rate limit
		{500, true}, // server error
		{501, true}, // server error
		{502, true}, // bad gateway
		{503, true}, // service unavailable
		{504, true}, // gateway timeout
		{599, true}, // server error range
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.retryable, err.IsRetryable(), "IsRetryable() for status %d", tt.statusCode)
		})
	}
}

func TestAPIError_IsNotFound(t *testing.T) {
	tests := []struct {
		statusCode int
		notFound   bool
	}{
		{200, false},
		{400, false},
		{404, true},
		{409, false},
		{500, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.notFound, err.IsNotFound(), "IsNotFound() for status %d", tt.statusCode)
		})
	}
}

func TestAPIError_IsConflict(t *testing.T) {
	tests := []struct {
		statusCode int
		conflict   bool
	}{
		{200, false},
		{400, false},
		{404, false},
		{409, true},
		{500, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.conflict, err.IsConflict(), "IsConflict() for status %d", tt.statusCode)
		})
	}
}

func TestAPIError_IsUnauthorized(t *testing.T) {
	tests := []struct {
		statusCode   int
		unauthorized bool
	}{
		{200, false},
		{401, true},
		{403, false},
		{404, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.unauthorized, err.IsUnauthorized(), "IsUnauthorized() for status %d", tt.statusCode)
		})
	}
}

func TestAPIError_IsForbidden(t *testing.T) {
	tests := []struct {
		statusCode int
		forbidden  bool
	}{
		{200, false},
		{401, false},
		{403, true},
		{404, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.forbidden, err.IsForbidden(), "IsForbidden() for status %d", tt.statusCode)
		})
	}
}

func TestAPIError_IsServerError(t *testing.T) {
	tests := []struct {
		statusCode  int
		serverError bool
	}{
		{200, false},
		{400, false},
		{404, false},
		{499, false},
		{500, true},
		{501, true},
		{503, true},
		{599, true},
		{600, false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.statusCode), func(t *testing.T) {
			err := &APIError{StatusCode: tt.statusCode, Method: "GET", Endpoint: "/test", Message: "test"}
			assert.Equal(t, tt.serverError, err.IsServerError(), "IsServerError() for status %d", tt.statusCode)
		})
	}
}

func TestIsAPIError(t *testing.T) {
	t.Run("with APIError", func(t *testing.T) {
		err := &APIError{StatusCode: 500, Method: "GET", Endpoint: "/test", Message: "fail"}
		assert.True(t, IsAPIError(err))
	})

	t.Run("with wrapped APIError", func(t *testing.T) {
		apiErr := &APIError{StatusCode: 500, Method: "GET", Endpoint: "/test", Message: "fail"}
		wrapped := fmt.Errorf("operation failed: %w", apiErr)
		assert.True(t, IsAPIError(wrapped))
	})

	t.Run("with generic error", func(t *testing.T) {
		err := errors.New("generic error")
		assert.False(t, IsAPIError(err))
	})

	t.Run("with nil error", func(t *testing.T) {
		assert.False(t, IsAPIError(nil))
	})
}

func TestAsAPIError(t *testing.T) {
	t.Run("with APIError", func(t *testing.T) {
		err := &APIError{StatusCode: 404, Method: "GET", Endpoint: "/test", Message: "not found"}
		apiErr, ok := AsAPIError(err)
		assert.True(t, ok)
		assert.Equal(t, 404, apiErr.StatusCode)
	})

	t.Run("with wrapped APIError", func(t *testing.T) {
		inner := &APIError{StatusCode: 409, Method: "PUT", Endpoint: "/test", Message: "conflict"}
		wrapped := fmt.Errorf("wrapper: %w", inner)
		apiErr, ok := AsAPIError(wrapped)
		assert.True(t, ok)
		assert.Equal(t, 409, apiErr.StatusCode)
		assert.Equal(t, "PUT", apiErr.Method)
	})

	t.Run("with generic error", func(t *testing.T) {
		err := errors.New("not an API error")
		_, ok := AsAPIError(err)
		assert.False(t, ok)
	})

	t.Run("with nil error", func(t *testing.T) {
		_, ok := AsAPIError(nil)
		assert.False(t, ok)
	})
}
