package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
)

// Клиент ушёл до ответа (уход со страницы, отмена запроса React Query): не 500 и не ERROR в журнале — иначе каждый
// такой запрос выглядит как сбой сервера (dev-журнал 23.11.2026: GET /api/club-map 500 «context canceled»).
func TestResponseErrorClientGone(t *testing.T) {
	canceled := func() *http.Request {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return httptest.NewRequest(http.MethodGet, "/api/club-map", nil).WithContext(ctx)
	}
	live := func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/club-map", nil) }

	cases := []struct {
		name      string
		req       *http.Request
		err       error
		status    int
		wantLevel string
	}{
		{"отмена клиентом", canceled(), fmt.Errorf("query: %w", context.Canceled), permissions.StatusClientClosedRequest, "level=INFO"},
		{"обрыв соединения при записи", live(), fmt.Errorf("write tcp: %w", syscall.EPIPE), permissions.StatusClientClosedRequest, "level=INFO"},
		{"сброс соединения", live(), fmt.Errorf("read: %w", syscall.ECONNRESET), permissions.StatusClientClosedRequest, "level=INFO"},
		// context.Canceled без отменённого запроса — отменил сам сервер (остановка, внутренний контекст): ошибка.
		{"Canceled при живом запросе", live(), context.Canceled, http.StatusInternalServerError, "level=ERROR"},
		// Дедлайн запроса (http.request_timeout) — медленный сервер, остаётся 5xx.
		{"дедлайн", live(), context.DeadlineExceeded, http.StatusInternalServerError, "level=ERROR"},
		{"прочая ошибка", live(), errors.New("boom"), http.StatusInternalServerError, "level=ERROR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			d := &Deps{Logger: slog.New(slog.NewTextHandler(&buf, nil))}
			w := httptest.NewRecorder()
			d.ResponseError()(w, c.req, c.err)
			if w.Code != c.status {
				t.Fatalf("статус %d, ждали %d", w.Code, c.status)
			}
			if !strings.Contains(buf.String(), c.wantLevel) {
				t.Fatalf("журнал %q, ждали %s", buf.String(), c.wantLevel)
			}
		})
	}
}
