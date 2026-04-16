package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Webhook delivery is additive to the inbox model: every match lands in
// subscription_events unconditionally; if the subscription has a webhook_url,
// we *also* try to push. A failed webhook never loses data — the agent can
// always catch up via the inbox.

const (
	webhookTimeout      = 10 * time.Second
	webhookMaxAttempts  = 6
	webhookResponseCap  = 1024 // bytes of response body we keep for debugging
	webhookDispatchChan = 128  // synchronous-attempt queue size
)

// Exponential backoff schedule: attempt N's next_retry_at is now + backoff[N-1].
// attempts=1 after first failure → wait 30s; attempts=6 → give up.
var webhookBackoff = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	1 * time.Hour,
	4 * time.Hour,
}

// allowedWebhookHost returns true if the URL is one we're willing to POST to.
// v1 policy: localhost loopback only. External URLs would need an opt-in flag
// and SSRF defenses (block private ranges, cloud metadata, etc.) — not yet built.
func allowedWebhookHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}
	// Accept literal loopback hostnames and loopback IPs.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("only localhost webhooks are allowed in this build")
}

// generateWebhookSecret returns a high-entropy secret shown once to the caller
// and stored plaintext for HMAC signing. Prefix "whsec_" so receivers can spot
// it in logs/configs.
func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// signWebhookBody computes the HMAC-SHA256 header value for a timestamp+body pair.
// Receivers verify by recomputing HMAC(timestamp + "." + body, secret).
func signWebhookBody(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// webhookWorker owns the retry loop and the concurrent dispatch workers.
// Lifecycle: start on server boot, stop via Shutdown() on graceful shutdown.
type webhookWorker struct {
	db            *sql.DB
	client        *http.Client
	dispatchQueue chan string // delivery_id — immediate-attempt hint; scanner is the safety net
	concurrency   int
	scanInterval  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newWebhookWorker(db *sql.DB) *webhookWorker {
	ctx, cancel := context.WithCancel(context.Background())
	return &webhookWorker{
		db: db,
		client: &http.Client{
			Timeout: webhookTimeout,
			// Never follow redirects — widens SSRF surface.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		dispatchQueue: make(chan string, webhookDispatchChan),
		concurrency:   4,
		scanInterval:  10 * time.Second,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
}

func (w *webhookWorker) Start() {
	go w.run()
}

// Shutdown signals workers to stop and waits for in-flight attempts to finish.
func (w *webhookWorker) Shutdown(ctx context.Context) error {
	w.cancel()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *webhookWorker) run() {
	defer close(w.done)

	// Spawn N concurrent deliverers.
	deliverDone := make(chan struct{}, w.concurrency)
	for i := 0; i < w.concurrency; i++ {
		go func() {
			defer func() { deliverDone <- struct{}{} }()
			w.deliverLoop()
		}()
	}

	// Scanner: every scanInterval, enqueue any deliveries due for retry.
	ticker := time.NewTicker(w.scanInterval)
	defer ticker.Stop()

	// Prime once on start (in case the server crashed mid-retry).
	w.enqueueDue()

	for {
		select {
		case <-w.ctx.Done():
			// Drain the workers.
			close(w.dispatchQueue)
			for i := 0; i < w.concurrency; i++ {
				<-deliverDone
			}
			return
		case <-ticker.C:
			w.enqueueDue()
		}
	}
}

func (w *webhookWorker) enqueueDue() {
	rows, err := w.db.QueryContext(w.ctx,
		`SELECT id FROM webhook_deliveries
		 WHERE next_retry_at IS NOT NULL AND next_retry_at <= ?
		   AND state IN ('pending','retrying')
		 ORDER BY next_retry_at ASC
		 LIMIT 100`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("webhook scanner: %v", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		select {
		case w.dispatchQueue <- id:
		case <-w.ctx.Done():
			return
		default:
			// Queue full — scanner will re-enqueue on its next tick.
		}
	}
}

func (w *webhookWorker) deliverLoop() {
	for id := range w.dispatchQueue {
		w.attempt(id)
	}
}

// enqueueNow is called synchronously after a webhook-bearing event is created.
// Non-blocking: if the queue is full, the scanner will pick it up shortly.
func (w *webhookWorker) enqueueNow(deliveryID string) {
	select {
	case w.dispatchQueue <- deliveryID:
	default:
	}
}

// attempt performs a single delivery attempt on a specific delivery row.
func (w *webhookWorker) attempt(deliveryID string) {
	ctx := w.ctx

	// Load delivery + event + subscription info.
	var (
		eventID, subID, url, secret, payloadJSON, action, resource, resourceID string
		sequence                                                               int64
		attempts                                                               int
		subDeleted                                                             sql.NullString
	)
	err := w.db.QueryRowContext(ctx, `
		SELECT d.event_id, d.subscription_id, d.url, s.webhook_secret, d.attempts,
		       e.sequence, e.payload, e.action, e.resource, e.resource_id,
		       s.deleted_at
		FROM webhook_deliveries d
		JOIN subscriptions s ON s.id = d.subscription_id
		JOIN subscription_events e ON e.id = d.event_id
		WHERE d.id = ?`, deliveryID,
	).Scan(&eventID, &subID, &url, &secret, &attempts, &sequence,
		&payloadJSON, &action, &resource, &resourceID, &subDeleted)
	if err != nil {
		log.Printf("webhook: load delivery %s: %v", deliveryID, err)
		return
	}

	// If the subscription was cancelled after the event fired, don't deliver.
	// (Inbox still has the event — the agent can catch up if it wants to.)
	if subDeleted.Valid {
		w.markTerminal(deliveryID, "failed", 0, "subscription cancelled")
		return
	}

	attemptNum := attempts + 1
	started := time.Now()

	// Build the body.
	var payload any
	_ = json.Unmarshal([]byte(payloadJSON), &payload)
	body := map[string]any{
		"event_id":        eventID,
		"subscription_id": subID,
		"sequence":        sequence,
		"resource":        resource,
		"resource_id":     resourceID,
		"action":          action,
		"payload":         payload,
	}
	bodyBytes, _ := json.Marshal(body)
	timestamp := time.Now().Unix()
	sig := signWebhookBody(secret, timestamp, bodyBytes)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		w.recordAttempt(deliveryID, attemptNum, 0, "", fmt.Sprintf("build request: %v", err), time.Since(started))
		w.scheduleRetry(deliveryID, attemptNum)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tasks127-Event-Id", eventID)
	req.Header.Set("X-Tasks127-Subscription-Id", subID)
	req.Header.Set("X-Tasks127-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Tasks127-Signature", sig)

	resp, err := w.client.Do(req)
	duration := time.Since(started)

	if err != nil {
		w.recordAttempt(deliveryID, attemptNum, 0, "", truncateError(err), duration)
		w.scheduleRetry(deliveryID, attemptNum)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, webhookResponseCap))
	succeeded := resp.StatusCode >= 200 && resp.StatusCode < 300

	w.recordAttempt(deliveryID, attemptNum, resp.StatusCode, string(respBody), "", duration)

	if succeeded {
		w.markTerminal(deliveryID, "delivered", resp.StatusCode, "")
		return
	}

	w.scheduleRetry(deliveryID, attemptNum)
}

func truncateError(err error) string {
	s := err.Error()
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// scheduleRetry updates the delivery row: if we still have attempts left, set
// next_retry_at to now + backoff[attemptNum-1]; otherwise mark terminal=failed.
func (w *webhookWorker) scheduleRetry(deliveryID string, attemptNum int) {
	if attemptNum >= webhookMaxAttempts {
		w.markTerminal(deliveryID, "failed", 0, "exhausted retries")
		return
	}
	backoff := webhookBackoff[attemptNum-1]
	nextRetry := time.Now().UTC().Add(backoff).Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := w.db.ExecContext(w.ctx,
		`UPDATE webhook_deliveries
		 SET state = 'retrying', attempts = ?, next_retry_at = ?, updated_at = ?
		 WHERE id = ?`,
		attemptNum, nextRetry, now, deliveryID)
	if err != nil {
		log.Printf("webhook: schedule retry %s: %v", deliveryID, err)
	}
}

func (w *webhookWorker) markTerminal(deliveryID, state string, statusCode int, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	var deliveredAt sql.NullString
	if state == "delivered" {
		deliveredAt = sql.NullString{String: now, Valid: true}
	}
	var errVal sql.NullString
	if errMsg != "" {
		errVal = sql.NullString{String: errMsg, Valid: true}
	}
	_, err := w.db.ExecContext(w.ctx,
		`UPDATE webhook_deliveries
		 SET state = ?, last_status_code = ?, last_error = ?,
		     next_retry_at = NULL, delivered_at = ?, updated_at = ?
		 WHERE id = ?`,
		state, nullableInt(statusCode), errVal, deliveredAt, now, deliveryID)
	if err != nil {
		log.Printf("webhook: mark terminal %s: %v", deliveryID, err)
	}
}

func (w *webhookWorker) recordAttempt(deliveryID string, num int, status int, respBody, errMsg string, duration time.Duration) {
	// Increment attempts counter unconditionally so subsequent retry scheduling
	// has the right number.
	_, err := w.db.ExecContext(w.ctx,
		`UPDATE webhook_deliveries SET attempts = ?, last_status_code = ?, last_error = ?, updated_at = ?
		 WHERE id = ?`,
		num, nullableInt(status), nullableStr(errMsg), time.Now().UTC().Format(time.RFC3339), deliveryID)
	if err != nil {
		log.Printf("webhook: update attempts %s: %v", deliveryID, err)
	}
	_, err = w.db.ExecContext(w.ctx,
		`INSERT INTO webhook_attempts (id, delivery_id, attempt_number, status_code, response_body, error, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ulid.Make().String(), deliveryID, num,
		nullableInt(status), nullableStr(respBody), nullableStr(errMsg), duration.Milliseconds())
	if err != nil {
		log.Printf("webhook: record attempt %s: %v", deliveryID, err)
	}
}

func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- delivery history endpoint ---

type webhookDeliveryResponse struct {
	ID             string  `json:"id"`
	EventID        string  `json:"event_id"`
	SubscriptionID string  `json:"subscription_id"`
	URL            string  `json:"url"`
	State          string  `json:"state"`
	Attempts       int     `json:"attempts"`
	LastStatusCode *int    `json:"last_status_code"`
	LastError      *string `json:"last_error"`
	NextRetryAt    *string `json:"next_retry_at"`
	DeliveredAt    *string `json:"delivered_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, err := s.getSubscription(r.Context(), id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canViewSubscription(r.Context(), sub)) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, event_id, subscription_id, url, state, attempts,
		        last_status_code, last_error, next_retry_at, delivered_at,
		        created_at, updated_at
		 FROM webhook_deliveries WHERE subscription_id = ?
		 ORDER BY created_at DESC LIMIT 50`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to list deliveries")
		return
	}
	defer rows.Close()

	var results []webhookDeliveryResponse
	for rows.Next() {
		var d webhookDeliveryResponse
		var statusCode sql.NullInt64
		var lastError, nextRetry, delivered sql.NullString
		if err := rows.Scan(&d.ID, &d.EventID, &d.SubscriptionID, &d.URL, &d.State, &d.Attempts,
			&statusCode, &lastError, &nextRetry, &delivered, &d.CreatedAt, &d.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		if statusCode.Valid {
			n := int(statusCode.Int64)
			d.LastStatusCode = &n
		}
		d.LastError = nullStr(lastError)
		d.NextRetryAt = nullStr(nextRetry)
		d.DeliveredAt = nullStr(delivered)
		results = append(results, d)
	}
	if results == nil {
		results = []webhookDeliveryResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  results,
		"count": len(results),
	})
}

// --- dispatch hook: called from fireEvents after an event row is inserted ---

// maybeCreateDelivery creates a webhook_deliveries row (and triggers immediate
// attempt) if the subscription has a webhook URL. Returns true if a delivery
// was scheduled.
func (s *Server) maybeCreateDelivery(ctx context.Context, subID, eventID, webhookURL string) bool {
	if webhookURL == "" {
		return false
	}
	id := ulid.Make().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (id, event_id, subscription_id, url, state, next_retry_at)
		 VALUES (?, ?, ?, ?, 'pending', ?)`,
		id, eventID, subID, webhookURL, now)
	if err != nil {
		log.Printf("webhook: create delivery for event %s: %v", eventID, err)
		return false
	}
	if s.webhookWorker != nil {
		s.webhookWorker.enqueueNow(id)
	}
	return true
}

// internal errors surfaced from URL validation.
var errWebhookURL = errors.New("invalid webhook URL")

// validateWebhookURL is the handler-facing wrapper that normalizes errors.
func validateWebhookURL(rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return nil // empty means no webhook; legal
	}
	if err := allowedWebhookHost(rawURL); err != nil {
		return fmt.Errorf("%w: %v", errWebhookURL, err)
	}
	return nil
}
