package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

const logPath = "/var/log/app/app.log"

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
		stdoutLogger.Printf(`{"message":"failed to marshal log","error":"%v"}`, err)
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

func main() {
	stdoutLogger, file, fileLogger, err := newLogger(logPath)
	if err != nil {
		log.Fatalf("cannot init logger: %v", err)
	}
	defer file.Close()

	mux := http.NewServeMux()
	mux.Handle("/hello", handleHello(stdoutLogger, fileLogger))

	handler := traceMiddleware(stdoutLogger, fileLogger, mux)

	server := &http.Server{
		Addr:         ":8080",
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	stdoutLogger.Println(`{"message":"server starting","addr":":8080"}`)
	fileLogger.Printf(`{"message":"server starting","addr":":8080"}\n`)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stdoutLogger.Fatalf(`{"message":"server error","error":"%v"}`, err)
	}
}
