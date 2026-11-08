// Package outbox delivers webhook events written by the store.
//
// Events are created in the same transaction as the state change they report
// (transactional outbox), so an event is never lost and never invented. Each
// retry reuses the event id as the Webhook-Id header, so receivers can drop
// repeats. Headers and signature follow the Standard Webhooks spec
// (https://www.standardwebhooks.com).
package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/salterhoper/smsmcp/server/internal/store"
)

// Backoff is the wait before attempt n+1 after n failures. After the last
// entry the delivery is marked dead.
var Backoff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
	30 * time.Minute, 1 * time.Hour, 3 * time.Hour, 6 * time.Hour,
}

type Worker struct {
	Store  *store.Store
	Client *http.Client
	Log    *slog.Logger
}

func New(st *store.Store, log *slog.Logger) *Worker {
	return &Worker{Store: st, Log: log, Client: &http.Client{Timeout: 15 * time.Second}}
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		due, err := w.Store.DueDeliveries(ctx, 50)
		if err != nil {
			w.Log.Error("due deliveries", "err", err)
			continue
		}
		for _, d := range due {
			w.deliver(ctx, d)
		}
	}
}

func (w *Worker) deliver(ctx context.Context, d store.DueDelivery) {
	ts := time.Now().Unix()
	sig, err := Sign(d.Secret, d.EventID, ts, []byte(d.Payload))
	if err != nil {
		w.Log.Error("sign", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader([]byte(d.Payload)))
	if err != nil {
		_ = w.Store.RecordDelivery(ctx, d.EventID, d.EndpointID, false, 0, err.Error(), 0)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "smsmcp-webhooks/1")
	req.Header.Set("Webhook-Id", d.EventID)
	req.Header.Set("Webhook-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("Webhook-Signature", sig)

	code, errMsg := 0, ""
	resp, err := w.Client.Do(req)
	if err != nil {
		errMsg = err.Error()
	} else {
		code = resp.StatusCode
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
	ok := code >= 200 && code < 300
	var next time.Duration
	if !ok && d.Attempts < len(Backoff) {
		next = Backoff[d.Attempts]
	}
	if err := w.Store.RecordDelivery(ctx, d.EventID, d.EndpointID, ok, code, errMsg, next); err != nil {
		w.Log.Error("record delivery", "err", err)
	}
	if !ok {
		w.Log.Warn("webhook failed", "event", d.EventID, "url", d.URL, "code", code, "err", errMsg, "retry_in", next)
	}
}

// Sign returns the Standard Webhooks signature header value:
// "v1," + base64(HMAC-SHA256(key, id + "." + timestamp + "." + body)).
func Sign(secret, id string, ts int64, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return "", fmt.Errorf("bad webhook secret: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%d.", id, ts)
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
