package collector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

type Collector struct {
	Store  *store.Store
	Client *http.Client
	slots  chan struct{}
	mu     sync.Mutex
	busy   map[int64]bool
}

func New(s *store.Store) *Collector {
	return &Collector{Store: s, Client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, slots: make(chan struct{}, 8), busy: map[int64]bool{}}
}

func (c *Collector) Scrape(ctx context.Context, t model.Target) (int, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	start := time.Now()
	count := 0
	err := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "text/plain; version=0.0.4")
		req.Header.Set("User-Agent", "MetricsPanel233/0.1")
		resp, err := c.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("exporter returned HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
		if err != nil {
			return err
		}
		if len(body) > 4*1024*1024 {
			return fmt.Errorf("exporter exceeds 4 MiB limit")
		}
		samples, err := ParsePrometheus(bytes.NewReader(body), t)
		if err != nil {
			return err
		}
		if err = c.Store.Ingest(ctx, samples); err != nil {
			return err
		}
		count = len(samples)
		return nil
	}()
	up := 1.0
	if err != nil {
		up = 0
	}
	labels := map[string]string{"job": t.Name, "instance": t.URL}
	for k, v := range t.Labels {
		labels[k] = v
	}
	upCtx, upCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	if saveErr := c.Store.Ingest(upCtx, []model.Sample{{Name: "up", Labels: labels, Value: up}, {Name: "scrape_duration_seconds", Labels: labels, Value: time.Since(start).Seconds()}, {Name: "scrape_samples_scraped", Labels: labels, Value: float64(count)}}); saveErr != nil {
		slog.Error("save scrape health", "error", saveErr)
	}
	upCancel()
	// Record failures even if the HTTP request's context expired.
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if saveErr := c.Store.TargetResult(resultCtx, t.ID, count, time.Since(start).Milliseconds(), err); saveErr != nil {
		slog.Error("save scrape result", "error", saveErr)
	}
	return count, err
}

func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			targets, err := c.Store.Targets(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("load collectors", "error", err)
				}
				continue
			}
			for _, t := range targets {
				if !t.Enabled || time.Now().UnixMilli()-t.LastScrape < int64(t.IntervalSeconds)*1000 {
					continue
				}
				c.mu.Lock()
				if c.busy[t.ID] {
					c.mu.Unlock()
					continue
				}
				c.busy[t.ID] = true
				c.mu.Unlock()
				jobs.Add(1)
				go func() {
					defer jobs.Done()
					defer func() { c.mu.Lock(); delete(c.busy, t.ID); c.mu.Unlock() }()
					if _, err := c.Scrape(ctx, t); err != nil && ctx.Err() == nil {
						slog.Warn("scrape failed", "target", t.Name, "error", err)
					}
				}()
			}
		}
	}
}
