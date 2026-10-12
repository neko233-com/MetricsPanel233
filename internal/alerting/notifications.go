package alerting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

func notificationBackoff(attempt int) time.Duration {
	delay := 5 * time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

// ProcessNotification delivers one claimed record. Delivery IDs remain stable
// across retries and are sent in both the payload and an HTTP header.
func (e *Engine) ProcessNotification(ctx context.Context, at time.Time) (bool, error) {
	delivery, raw, err := e.Store.ClaimNotification(ctx, at.UnixMilli())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state, message, code := "pending", "", 0
	point, err := e.Store.ContactPoint(ctx, delivery.ContactPointUID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && point.Disabled) {
		state, message = "canceled", "Contact point unavailable or disabled"
	} else if err != nil {
		message = "Cannot load contact point"
	} else {
		secrets, err := e.Store.ContactPointSecrets(ctx, point.UID)
		if err != nil {
			message = "Cannot decrypt contact point credentials"
		} else {
			prepared, frozen, err := e.prepareTemplateNotification(ctx, delivery, raw)
			if err != nil {
				message = "Cannot prepare notification template snapshot"
				if errors.Is(err, errSavedTemplateRender) {
					state, message = "failed", "Notification template rendering failed for the saved version"
				}
			} else {
				code, message = e.deliverNotification(ctx, delivery, point, secrets, prepared, frozen)
				if message == "" {
					state = "sent"
				}
			}
		}
	}
	if ctx.Err() != nil {
		// The server's canceled context must not prevent releasing its lease.
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return true, e.Store.ReleaseNotification(releaseCtx, delivery)
	}
	if state == "pending" && delivery.Attempts >= delivery.MaxAttempts {
		state = "failed"
	}
	if state == "pending" && code >= 300 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooEarly && code != http.StatusTooManyRequests {
		state = "failed"
	}
	finishedAt := time.Now()
	if finishedAt.Before(at) {
		finishedAt = at
	}
	next := int64(0)
	if state == "pending" {
		next = finishedAt.Add(notificationBackoff(delivery.Attempts)).UnixMilli()
	}
	return true, e.Store.FinishNotification(ctx, delivery, state, message, code, next, finishedAt.UnixMilli())
}

func (e *Engine) deliverNotification(ctx context.Context, delivery model.NotificationDelivery, point model.ContactPoint, secrets map[string]string, raw json.RawMessage, frozen bool) (int, string) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, "Invalid stored notification payload"
	}
	payload["notificationId"] = strconv.FormatInt(delivery.ID, 10)
	if e.TemplateURL != nil && !frozen {
		payload["externalURL"] = e.TemplateURL.String()
		if alerts, ok := payload["alerts"].([]any); ok {
			for _, item := range alerts {
				if alert, ok := item.(map[string]any); ok {
					alert["generatorURL"] = e.TemplateURL.String() + "/#alerts"
				}
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "Cannot encode notification payload"
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(point.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, point.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "Invalid webhook request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MetricsPanel233-Alerting")
	req.Header.Set("X-Metricspanel-Notification-Id", strconv.FormatInt(delivery.ID, 10))
	for key, value := range secrets {
		if strings.HasPrefix(key, "header:") {
			name := strings.TrimPrefix(key, "header:")
			if model.ValidNotificationHeader(name, value) {
				req.Header.Set(name, value)
			}
		}
	}
	if token := secrets["bearer_token"]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: time.Duration(point.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		// Transport errors can embed the URL or credentials. Persist only a
		// fixed diagnostic; never retain receiver response bodies either.
		if requestCtx.Err() == context.DeadlineExceeded {
			return 0, "Webhook request timed out"
		}
		return 0, "Webhook request failed"
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8192))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Sprintf("Webhook returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, ""
}

func (e *Engine) RunNotifications(ctx context.Context) {
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for range 8 {
						processed, err := e.ProcessNotification(ctx, time.Now())
						if err != nil {
							if ctx.Err() == nil {
								slog.Error("dispatch alert notification", "error", err)
							}
							break
						}
						if !processed || ctx.Err() != nil {
							break
						}
					}
				}
			}
		}()
	}
	workers.Wait()
}
