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
}

func parseConfig() config {
	var cfg config
	flag.StringVar(&cfg.target, "target", envOrDefault("TARGET_URL", "http://localhost:8080/hello"), "target URL")
	flag.IntVar(&cfg.total, "count", 20, "total requests to send")
	flag.IntVar(&cfg.concurrency, "concurrency", 2, "number of concurrent workers")
	flag.DurationVar(&cfg.interval, "interval", 500*time.Millisecond, "delay between requests per worker")
	flag.DurationVar(&cfg.timeout, "timeout", 3*time.Second, "HTTP client timeout")
	flag.Parse()
	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func worker(id int, cfg config, jobs <-chan int, client *http.Client, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		traceID := uuid.NewString()
		req, err := http.NewRequest(http.MethodGet, cfg.target, nil)
		if err != nil {
			log.Printf("[worker %d] build req err: %v", id, err)
			continue
		}
		req.Header.Set("X-Trace-Id", traceID)

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start)
		if err != nil {
			log.Printf("[worker %d] request %d failed (trace %s): %v", id, job, traceID, err)
			time.Sleep(cfg.interval)
			continue
		}

		_ = resp.Body.Close()
		log.Printf("[worker %d] request %d ok (trace %s) status=%d latency=%s", id, job, traceID, resp.StatusCode, latency)
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
