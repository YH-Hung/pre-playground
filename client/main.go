package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type config struct {
	target      string
	total       int
	concurrency int
	interval    time.Duration
	timeout     time.Duration
	maxRetries  int
}

func parseConfig() config {
	var cfg config
	flag.StringVar(&cfg.target, "target", envOrDefault("TARGET_URL", "http://localhost:8080/hello"), "target URL")
	flag.IntVar(&cfg.total, "count", parseIntEnv("CLIENT_COUNT", 20), "total requests to send")
	flag.IntVar(&cfg.concurrency, "concurrency", parseIntEnv("CLIENT_CONCURRENCY", 2), "number of concurrent workers")
	flag.DurationVar(&cfg.interval, "interval", parseDurationEnv("CLIENT_INTERVAL", 500*time.Millisecond), "delay between requests per worker")
	flag.DurationVar(&cfg.timeout, "timeout", parseDurationEnv("CLIENT_TIMEOUT", 3*time.Second), "HTTP client timeout")
	flag.IntVar(&cfg.maxRetries, "retries", parseIntEnv("CLIENT_MAX_RETRIES", 3), "maximum retry attempts for failed requests")
	flag.Parse()
	return cfg
}

func parseIntEnv(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := fmt.Sscanf(v, "%d", &defaultValue); err == nil && parsed == 1 {
			return defaultValue
		}
	}
	return defaultValue
}

func parseDurationEnv(key string, defaultValue time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			return parsed
		}
	}
	return defaultValue
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func isRetryableError(err error, statusCode int) bool {
	if err != nil {
		return true // Network errors are retryable
	}
	// 5xx errors are retryable, 4xx (except 429) are not
	return statusCode >= 500 || statusCode == 429
}

func doRequestWithRetry(id int, job int, cfg config, client *http.Client, traceID string) (bool, time.Duration) {
	var lastErr error
	var lastStatusCode int

	for attempt := 0; attempt <= cfg.maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodGet, cfg.target, nil)
		if err != nil {
			log.Printf("[worker %d] request %d build error (trace %s): %v", id, job, traceID, err)
			return false, 0
		}
		req.Header.Set("X-Trace-Id", traceID)

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)

		if err != nil {
			lastErr = err
			lastStatusCode = 0
		} else {
			lastStatusCode = resp.StatusCode
			_ = resp.Body.Close()
		}

		// Success case
		if err == nil && lastStatusCode < 400 {
			if attempt > 0 {
				log.Printf("[worker %d] request %d succeeded on retry %d (trace %s) status=%d latency=%s",
					id, job, attempt, traceID, lastStatusCode, latency)
			}
			return true, latency
		}

		// Check if retryable
		if !isRetryableError(err, lastStatusCode) {
			log.Printf("[worker %d] request %d failed non-retryable (trace %s) status=%d: %v",
				id, job, traceID, lastStatusCode, err)
			return false, latency
		}

		// If not last attempt, wait with exponential backoff
		if attempt < cfg.maxRetries {
			backoff := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
			log.Printf("[worker %d] request %d failed (trace %s) attempt %d/%d, retrying in %v: %v",
				id, job, traceID, attempt+1, cfg.maxRetries+1, backoff, err)
			time.Sleep(backoff)
		}
	}

	// All retries exhausted
	log.Printf("[worker %d] request %d failed after %d retries (trace %s) status=%d: %v",
		id, job, cfg.maxRetries, traceID, lastStatusCode, lastErr)
	return false, 0
}

func worker(id int, cfg config, jobs <-chan int, client *http.Client, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		traceID := uuid.NewString()
		success, latency := doRequestWithRetry(id, job, cfg, client, traceID)

		if success {
			log.Printf("[worker %d] request %d ok (trace %s) latency=%s", id, job, traceID, latency)
		}

		time.Sleep(cfg.interval)
	}
}

func main() {
	cfg := parseConfig()
	log.Printf("starting client target=%s total=%d concurrency=%d interval=%s", cfg.target, cfg.total, cfg.concurrency, cfg.interval)

	client := &http.Client{Timeout: cfg.timeout}
	jobs := make(chan int, cfg.total)

	var wg sync.WaitGroup
	for i := 0; i < cfg.concurrency; i++ {
		wg.Add(1)
		go worker(i, cfg, jobs, client, &wg)
	}

	for i := 0; i < cfg.total; i++ {
		jobs <- i + 1
	}
	close(jobs)

	wg.Wait()
	fmt.Println("client finished")
}
