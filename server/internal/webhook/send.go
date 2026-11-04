package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Заголовки и подпись — docs/integration-api.md старого проекта, app/Jobs/DeliverWebhook.php send().
const (
	HeaderEvent     = "X-Zafiro-Event"
	HeaderDelivery  = "X-Zafiro-Delivery"
	HeaderTimestamp = "X-Zafiro-Timestamp"
	HeaderSignature = "X-Zafiro-Signature"
	UserAgent       = "Zafiro-Webhooks/1"
)

// Sign — "v1=" + hex(HMAC-SHA256(secret, "{timestamp}.{body}")).
// Laravel: app/Jobs/DeliverWebhook.php DeliverWebhook::send() — hash_hmac('sha256', "{$timestamp}.{$body}", $secret).
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify — проверка подписи получателем (для тестов и документации): подпись совпадает и метка времени
// не старше tolerance (integrations.webhook_signature_tolerance_seconds = 300).
func Verify(secret string, timestampHeader, signatureHeader string, body []byte, now time.Time, tolerance time.Duration) bool {
	ts, err := strconv.ParseInt(timestampHeader, 10, 64)
	if err != nil {
		return false
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, ts, body)), []byte(signatureHeader))
}

// request — одна отправка.
type request struct {
	URL       string
	Target    Target
	Secret    string
	Event     string
	Delivery  string
	Body      []byte
	Timestamp int64
}

// sender — HTTP-клиент доставки: таймаут ответа Timeout (webhook_timeout_seconds = 5), соединения —
// ConnectTimeout (3 с), без редиректов, без прокси из окружения, соединение — только на проверенный IP.
type sender struct {
	Timeout        time.Duration
	ConnectTimeout time.Duration
}

// send возвращает HTTP-статус (0 — ответа не было) и ошибку для last_error ("" — успех, 2xx).
// Тексты ошибок — как в старом: "connection failed: ConnectionException", "HTTP 500".
func (s sender) send(ctx context.Context, r request) (int, string) {
	pinned := net.JoinHostPort(r.Target.IP.String(), strconv.Itoa(r.Target.Port))
	dialer := &net.Dialer{Timeout: s.ConnectTimeout}
	transport := &http.Transport{
		Proxy: nil, // прокси обошёл бы пиннинг адреса
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, pinned)
		},
		TLSHandshakeTimeout:    s.ConnectTimeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   s.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // withoutRedirecting()
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return 0, "connection failed: ConnectionException"
	}
	ts := strconv.FormatInt(r.Timestamp, 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set(HeaderEvent, r.Event)
	req.Header.Set(HeaderDelivery, r.Delivery)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, Sign(r.Secret, r.Timestamp, r.Body))

	resp, err := client.Do(req) // #nosec G704 -- адрес проверен Guard.Check и пришпилен в DialContext
	if err != nil {
		// Laravel ловил Throwable и писал class_basename($e) — у Http-клиента это ConnectionException
		// (и таймаут, и отказ соединения).
		if errors.Is(err, context.Canceled) {
			return 0, "connection failed: Canceled"
		}
		return 0, "connection failed: ConnectionException"
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 { // $response->successful()
		return resp.StatusCode, ""
	}
	return resp.StatusCode, "HTTP " + strconv.Itoa(resp.StatusCode)
}
