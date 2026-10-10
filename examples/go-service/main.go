// A real Go backend exposing Prometheus metrics and periodically pushing a
// business metric. Used by the Docker integration test and as an integration
// example; failures are logged and retried on the next tick.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

func main() {
	var requests atomic.Int64
	var pushes atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprintln(w, "Go business backend is running")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE business_http_requests_total counter\nbusiness_http_requests_total{service=\"checkout\"} %d\n# TYPE business_orders gauge\nbusiness_orders{service=\"checkout\"} %d\n", requests.Load(), pushes.Load())
	})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		client := &http.Client{Timeout: 5 * time.Second}
		for range ticker.C {
			n := pushes.Add(1)
			body, _ := json.Marshal(map[string]any{"samples": []any{map[string]any{"name": "business_push_total", "labels": map[string]string{"service": "checkout", "source": "go"}, "value": n}}})
			req, err := http.NewRequest("POST", os.Getenv("METRICSPANEL_URL")+"/api/v1/ingest", bytes.NewReader(body))
			if err != nil {
				slog.Error("push request", "error", err)
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+os.Getenv("METRICSPANEL_TOKEN"))
			resp, err := client.Do(req)
			if err != nil {
				slog.Warn("push failed", "error", err)
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				slog.Warn("push rejected", "status", resp.StatusCode)
			}
		}
	}()
	address := os.Getenv("GO_SERVICE_LISTEN")
	if address == "" {
		address = ":8080"
	}
	slog.Info("example Go backend", "address", address)
	if err := http.ListenAndServe(address, mux); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
