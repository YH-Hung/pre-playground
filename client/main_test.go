package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		want       bool
	}{
		{"network error", &timeoutError{}, 0, true},
		{"500 error", nil, 500, true},
		{"502 error", nil, 502, true},
		{"429 error", nil, 429, true},
		{"400 error", nil, 400, false},
		{"404 error", nil, 404, false},
		{"200 success", nil, 200, false},
		{"nil error 200", nil, 200, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err, tt.statusCode)
			if got != tt.want {
				t.Errorf("isRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

type timeoutError struct{}

func (e *timeoutError) Error() string { return "timeout" }
func (e *timeoutError) Timeout() bool { return true }

func TestParseIntEnv(t *testing.T) {
	// Test with default value
	result := parseIntEnv("NONEXISTENT_VAR", 42)
	if result != 42 {
		t.Errorf("expected 42, got %d", result)
	}

	// Test with environment variable
	os.Setenv("TEST_INT", "100")
	defer os.Unsetenv("TEST_INT")
	result = parseIntEnv("TEST_INT", 42)
	if result != 100 {
		t.Errorf("expected 100, got %d", result)
	}

	// Test with invalid value (should return default)
	os.Setenv("TEST_INT_INVALID", "not-a-number")
	defer os.Unsetenv("TEST_INT_INVALID")
	result = parseIntEnv("TEST_INT_INVALID", 42)
	if result != 42 {
		t.Errorf("expected default 42, got %d", result)
	}
}

func TestParseDurationEnv(t *testing.T) {
	// Test with default value
	defaultDur := 5 * time.Second
	result := parseDurationEnv("NONEXISTENT_VAR", defaultDur)
	if result != defaultDur {
		t.Errorf("expected %v, got %v", defaultDur, result)
	}

	// Test with environment variable
	os.Setenv("TEST_DUR", "10s")
	defer os.Unsetenv("TEST_DUR")
	result = parseDurationEnv("TEST_DUR", defaultDur)
	if result != 10*time.Second {
		t.Errorf("expected 10s, got %v", result)
	}

	// Test with invalid value (should return default)
	os.Setenv("TEST_DUR_INVALID", "invalid")
	defer os.Unsetenv("TEST_DUR_INVALID")
	result = parseDurationEnv("TEST_DUR_INVALID", defaultDur)
	if result != defaultDur {
		t.Errorf("expected default %v, got %v", defaultDur, result)
	}
}

func TestDoRequestWithRetry_Success(t *testing.T) {
	// Create a test server that succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	cfg := config{
		target:     server.URL,
		maxRetries: 3,
	}
	client := &http.Client{Timeout: 5 * time.Second}

	success, latency := doRequestWithRetry(1, 1, cfg, client, "test-trace")
	if !success {
		t.Error("expected request to succeed")
	}
	if latency <= 0 {
		t.Error("expected positive latency")
	}
}

func TestDoRequestWithRetry_RetryableFailure(t *testing.T) {
	attempts := 0
	// Create a test server that fails twice then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	cfg := config{
		target:     server.URL,
		maxRetries: 3,
	}
	client := &http.Client{Timeout: 5 * time.Second}

	success, _ := doRequestWithRetry(1, 1, cfg, client, "test-trace")
	if !success {
		t.Error("expected request to succeed after retries")
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts, got %d", attempts)
	}
}

func TestDoRequestWithRetry_NonRetryableFailure(t *testing.T) {
	// Create a test server that returns 400 (non-retryable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := config{
		target:     server.URL,
		maxRetries: 3,
	}
	client := &http.Client{Timeout: 5 * time.Second}

	success, _ := doRequestWithRetry(1, 1, cfg, client, "test-trace")
	if success {
		t.Error("expected request to fail (non-retryable)")
	}
}

func TestDoRequestWithRetry_ExhaustRetries(t *testing.T) {
	// Create a test server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := config{
		target:     server.URL,
		maxRetries: 2,
	}
	client := &http.Client{Timeout: 5 * time.Second}

	success, _ := doRequestWithRetry(1, 1, cfg, client, "test-trace")
	if success {
		t.Error("expected request to fail after exhausting retries")
	}
}
