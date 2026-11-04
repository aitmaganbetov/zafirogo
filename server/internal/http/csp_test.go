package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Хеш inline-скриптов web/index.html должен быть в CSP, иначе браузер их заблокирует.
func TestCSPCoversInlineScripts(t *testing.T) {
	html, err := os.ReadFile("../../../web/index.html")
	if err != nil {
		t.Skipf("web/index.html недоступен: %v", err)
	}
	re := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	matches := re.FindAllSubmatch(html, -1)
	if len(matches) == 0 {
		t.Fatal("inline-скрипт темы не найден в web/index.html")
	}
	for _, m := range matches {
		sum := sha256.Sum256(m[1])
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(contentSecurityPolicy, hash) {
			t.Errorf("CSP не содержит %s для скрипта %q", hash, m[1])
		}
	}
}

// SEC-019: WebSocket в CSP — только свой хост, не любой ws:/wss:.
func TestCSPConnectSrcOwnHostOnly(t *testing.T) {
	csp := cspFor("club.zafiro.kz")
	if strings.Contains(csp, "ws: ") || strings.Contains(csp, "wss:;") || strings.Contains(csp, "wss: ") {
		t.Fatalf("connect-src allows any WebSocket host: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' wss://club.zafiro.kz ws://club.zafiro.kz;") {
		t.Fatalf("connect-src: %s", csp)
	}
	if got := cspFor("evil;script-src *"); strings.Contains(got, "evil") {
		t.Fatalf("host injected into CSP: %s", got)
	}
	if strings.Contains(cspFor("x"), cspHostPlaceholder) {
		t.Fatal("placeholder left in CSP")
	}
}

// SEC-018: HSTS и за TLS-прокси (запрос до процесса идёт по HTTP, r.TLS == nil).
func TestHSTSBehindProxy(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "http://club.zafiro.kz/", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Fatalf("HSTS = %q", got)
	}
}
