package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	defaultLogPath         = "/var/log/app/app.log"
	defaultPort            = "8080"
	defaultShutdownTimeout = 10 * time.Second
)

var (
	// Metrics for observability
	requestCount   int64
	errorCount     int64
	totalLatencyMs int64
	metricsMutex   sync.RWMutex
)

type ctxKey string

const traceKey ctxKey = "traceId"

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

type logEntry struct {
	TraceID   string `json:"traceId"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	Message   string `json:"message"`
}

func ensureLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func newLogger(path string) (*log.Logger, *os.File, *log.Logger, error) {
	f, err := ensureLogFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	// Write to stdout with timestamp for docker logs, file without timestamp for Fluent Bit parsing
	stdoutLogger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)
	fileLogger := log.New(f, "", 0) // No timestamp prefix for clean JSON
	return stdoutLogger, f, fileLogger, nil
}

func traceMiddleware(stdoutLogger *log.Logger, fileLogger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		traceID := r.Header.Get("X-Trace-Id")
		if traceID == "" {
			traceID = uuid.NewString()
		}

		ctx := context.WithValue(r.Context(), traceKey, traceID)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r.WithContext(ctx))

		latency := time.Since(start)

		// Update metrics
		metricsMutex.Lock()
		requestCount++
		if rec.status >= 400 {
			errorCount++
		}
		totalLatencyMs += latency.Milliseconds()
		metricsMutex.Unlock()

		logJSON(stdoutLogger, fileLogger, logEntry{
			TraceID:   traceID,
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    rec.status,
			LatencyMs: latency.Milliseconds(),
			Message:   "request completed",
		})
	})
}

func logJSON(stdoutLogger *log.Logger, fileLogger *log.Logger, entry logEntry) {
	b, err := json.Marshal(entry)
	if err != nil {
		stdoutLogger.Printf(`{"message":"failed to marshal log","error":"%v"}\n`, err)
		fileLogger.Printf(`{"message":"failed to marshal log","error":"%v"}\n`, err)
		return
	}
	// Write to stdout with timestamp, file without timestamp (pure JSON)
	stdoutLogger.Println(string(b))
	fileLogger.Printf("%s\n", string(b))
}

func handleHello(stdoutLogger *log.Logger, fileLogger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID, _ := r.Context().Value(traceKey).(string)
		resp := map[string]string{
			"message": "hello",
			"traceId": traceID,
			"path":    r.URL.Path,
		}
		time.Sleep(50 * time.Millisecond) // simulate work

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			logJSON(stdoutLogger, fileLogger, logEntry{
				TraceID: traceID,
				Method:  r.Method,
				Path:    r.URL.Path,
				Status:  http.StatusInternalServerError,
				Message: "failed to encode response",
			})
			return
		}

		logJSON(stdoutLogger, fileLogger, logEntry{
			TraceID: traceID,
			Method:  r.Method,
			Path:    r.URL.Path,
			Status:  http.StatusOK,
			Message: "handler finished",
		})
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"service": "prr-playground-server",
	})
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	metricsMutex.RLock()
	defer metricsMutex.RUnlock()

	var avgLatencyMs int64
	if requestCount > 0 {
		avgLatencyMs = totalLatencyMs / requestCount
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "# HELP http_requests_total Total number of HTTP requests\n")
	fmt.Fprintf(w, "# TYPE http_requests_total counter\n")
	fmt.Fprintf(w, "http_requests_total %d\n", requestCount)
	fmt.Fprintf(w, "# HELP http_errors_total Total number of HTTP errors (4xx, 5xx)\n")
	fmt.Fprintf(w, "# TYPE http_errors_total counter\n")
	fmt.Fprintf(w, "http_errors_total %d\n", errorCount)
	fmt.Fprintf(w, "# HELP http_request_duration_ms Average request latency in milliseconds\n")
	fmt.Fprintf(w, "# TYPE http_request_duration_ms gauge\n")
	fmt.Fprintf(w, "http_request_duration_ms %d\n", avgLatencyMs)
}

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func main() {
	// Configuration from environment variables
	logPath := getEnvOrDefault("LOG_PATH", defaultLogPath)
	port := getEnvOrDefault("PORT", defaultPort)
	shutdownTimeoutStr := getEnvOrDefault("SHUTDOWN_TIMEOUT", defaultShutdownTimeout.String())
	shutdownTimeout, err := time.ParseDuration(shutdownTimeoutStr)
	if err != nil {
		shutdownTimeout = defaultShutdownTimeout
	}

	stdoutLogger, file, fileLogger, err := newLogger(logPath)
	if err != nil {
		log.Fatalf("cannot init logger: %v", err)
	}
	defer func() {
		// Ensure file is synced and closed on exit
		if err := file.Sync(); err != nil {
			stdoutLogger.Printf(`{"message":"failed to sync log file","error":"%v"}`, err)
		}
		if err := file.Close(); err != nil {
			stdoutLogger.Printf(`{"message":"failed to close log file","error":"%v"}`, err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/hello", handleHello(stdoutLogger, fileLogger))
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/metrics", handleMetrics)

	handler := traceMiddleware(stdoutLogger, fileLogger, mux)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Channel to listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	serverErrChan := make(chan error, 1)
	go func() {
		stdoutLogger.Printf(`{"message":"server starting","addr":":%s"}`, port)
		fileLogger.Printf(`{"message":"server starting","addr":":%s"}\n`, port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrChan <- err
		}
	}()

	// Wait for interrupt signal or server error
	select {
	case err := <-serverErrChan:
		stdoutLogger.Fatalf(`{"message":"server error","error":"%v"}`, err)
	case sig := <-sigChan:
		stdoutLogger.Printf(`{"message":"received signal","signal":"%v","shutting_down":true}`, sig)
		fileLogger.Printf(`{"message":"received signal","signal":"%v","shutting_down":true}\n`, sig)

		// Create shutdown context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		// Graceful shutdown
		if err := server.Shutdown(ctx); err != nil {
			stdoutLogger.Printf(`{"message":"server shutdown error","error":"%v"}`, err)
			fileLogger.Printf(`{"message":"server shutdown error","error":"%v"}\n`, err)
			// Force close if graceful shutdown fails
			server.Close()
		} else {
			stdoutLogger.Println(`{"message":"server shutdown gracefully"}`)
			fileLogger.Printf(`{"message":"server shutdown gracefully"}\n`)
		}

		// Final sync of log file
		if err := file.Sync(); err != nil {
			stdoutLogger.Printf(`{"message":"failed to sync log file on shutdown","error":"%v"}`, err)
		}
	}
}
