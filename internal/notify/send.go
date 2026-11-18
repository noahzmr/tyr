package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Notifier sends events to the configured webhooks in the background, so
// a slow or unreachable receiver never holds up a test.
type Notifier struct {
	hooks   []Webhook
	log     func(string, ...any)
	wg      sync.WaitGroup
	backoff []time.Duration
	timeout time.Duration
	client  func(insecure bool) *http.Client
}

// NewNotifier makes a notifier; log gets one line per delivery problem
// (nil: silent). A nil or empty config sends nothing.
func NewNotifier(c Config, log func(string, ...any)) *Notifier {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Notifier{hooks: c.Webhooks, log: log, backoff: []time.Duration{0, 5 * time.Second, 30 * time.Second},
		timeout: 15 * time.Second, client: httpClient}
}

func httpClient(insecure bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the user asked for it
	}
	return &http.Client{Transport: tr}
}

// Enabled reports whether any webhook is configured.
func (n *Notifier) Enabled() bool { return n != nil && len(n.hooks) > 0 }

// Send delivers the event to every webhook that wants it, in the
// background.
func (n *Notifier) Send(e Event) {
	if !n.Enabled() {
		return
	}
	for _, w := range n.hooks {
		if !w.Wants(e.Event) {
			continue
		}
		n.wg.Add(1)
		go func(w Webhook) {
			defer n.wg.Done()
			if err := n.deliver(context.Background(), w, e); err != nil {
				n.log("webhook %s: %s not delivered: %v", w.Label(), e.Event, err)
			}
		}(w)
	}
}

// Deliver sends the event to every webhook that wants it and waits;
// it returns the errors per webhook (tyr notify --test).
func (n *Notifier) Deliver(ctx context.Context, e Event) map[string]error {
	out := map[string]error{}
	for _, w := range n.hooks {
		if w.Wants(e.Event) {
			out[w.Label()] = n.deliver(ctx, w, e)
		}
	}
	return out
}

// Wait waits for the deliveries in flight, at most d.
func (n *Notifier) Wait(d time.Duration) {
	if n == nil {
		return
	}
	done := make(chan struct{})
	go func() { n.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		n.log("webhooks: gave up waiting after %s", d)
	}
}

// deliver posts with retries: a network error or a 5xx/429 is tried again.
func (n *Notifier) deliver(ctx context.Context, w Webhook, e Event) error {
	body, err := e.Body(w.Format)
	if err != nil {
		return err
	}
	var last error
	for i, wait := range n.backoff {
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		retry, err := n.post(ctx, w, e.Event, body)
		if err == nil {
			return nil
		}
		last = err
		if !retry {
			break
		}
		if i < len(n.backoff)-1 {
			n.log("webhook %s: %v - trying again", w.Label(), err)
		}
	}
	return last
}

func (n *Notifier) post(ctx context.Context, w Webhook, event string, body []byte) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tyr")
	req.Header.Set("X-Tyr-Event", event)
	if w.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.Token)
	}
	if w.Secret != "" {
		req.Header.Set("X-Tyr-Signature", "sha256="+Sign(w.Secret, body))
	}
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client(w.Insecure).Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 == 2 {
		return false, nil
	}
	return resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests,
		fmt.Errorf("HTTP %d %s", resp.StatusCode, bytes.TrimSpace(msg))
}

// Sign is the hex HMAC-SHA256 of the body with the secret: what a receiver
// compares with X-Tyr-Signature (after "sha256=").
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
