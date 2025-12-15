package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHandleHealth(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got '%s'", response["status"])
	}
}

func TestHandleMetrics(t *testing.T) {
	// Reset metrics
	metricsMutex.Lock()
	requestCount = 0
	errorCount = 0
	totalLatencyMs = 0
	metricsMutex.Unlock()

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	handleMetrics(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "http_requests_total") {
		t.Error("metrics output should contain http_requests_total")
	}
	if !strings.Contains(body, "http_errors_total") {
		t.Error("metrics output should contain http_errors_total")
	}
	if !strings.Contains(body, "http_request_duration_ms") {
		t.Error("metrics output should contain http_request_duration_ms")
	}
}

func TestTraceMiddleware(t *testing.T) {
	// Create temporary log file
	tmpFile, err := os.CreateTemp("", "test-*.log")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	stdoutLogger := log.New(os.Stdout, "", 0)
	fileLogger := log.New(os.Stdout, "", 0)

	handler := traceMiddleware(stdoutLogger, fileLogger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Context().Value(traceKey)
		if traceID == nil {
			t.Error("traceId not found in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Trace-Id", "test-trace-123")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	// Verify metrics were updated
	metricsMutex.RLock()
	if requestCount == 0 {
		t.Error("requestCount should be incremented")
	}
	metricsMutex.RUnlock()
}

func TestHandleHello(t *testing.T) {
	stdoutLogger := log.New(os.Stdout, "", 0)
	fileLogger := log.New(os.Stdout, "", 0)

	handler := handleHello(stdoutLogger, fileLogger)

	req := httptest.NewRequest("GET", "/hello", nil)
	ctx := context.WithValue(req.Context(), traceKey, "test-trace-123")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["message"] != "hello" {
		t.Errorf("expected message 'hello', got '%s'", response["message"])
	}
	if response["traceId"] != "test-trace-123" {
		t.Errorf("expected traceId 'test-trace-123', got '%s'", response["traceId"])
	}
}

func TestGetEnvOrDefault(t *testing.T) {
	// Test with default value
	result := getEnvOrDefault("NONEXISTENT_VAR", "default")
	if result != "default" {
		t.Errorf("expected 'default', got '%s'", result)
	}

	// Test with environment variable
	os.Setenv("TEST_VAR", "test-value")
	defer os.Unsetenv("TEST_VAR")
	result = getEnvOrDefault("TEST_VAR", "default")
	if result != "test-value" {
		t.Errorf("expected 'test-value', got '%s'", result)
	}
}

func TestStatusRecorder(t *testing.T) {
	w := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

	if rec.status != http.StatusOK {
		t.Errorf("expected initial status %d, got %d", http.StatusOK, rec.status)
	}

	rec.WriteHeader(http.StatusNotFound)
	if rec.status != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.status)
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("expected response writer status %d, got %d", http.StatusNotFound, w.Code)
	}
}
