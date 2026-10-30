package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
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
