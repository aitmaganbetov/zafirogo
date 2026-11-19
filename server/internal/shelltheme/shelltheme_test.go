package shelltheme

import (
	"regexp"
	"slices"
	"testing"
)

// Laravel: app/Support/ShellThemes.php — порядок ключей, цвета для агента и свотчи (тесты старого
// tests/Feature/ShellThemeTest.php: red → accent #dc2626, bg_0 #0a0304).
func TestThemes(t *testing.T) {
	if got := Keys(); !slices.Equal(got, []string{"standard", "blue", "green", "red", "orange", "teal", "pink"}) {
		t.Fatalf("Keys = %v", got)
	}
	if For(Default) != nil || For("") != nil || For("purple") != nil {
		t.Fatal("standard / неизвестная тема — без цветов (агент остаётся в своей)")
	}
	red := For("red")
	if red == nil || red.Accent != "#dc2626" || red.Bg0 != "#0a0304" || red.Bg1 != "#1f0709" {
		t.Fatalf("red = %+v", red)
	}
	red.Accent = "x" // копия: таблица тем не меняется
	if For("red").Accent != "#dc2626" {
		t.Fatal("For отдал общую структуру")
	}
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for _, k := range Keys() {
		if !Valid(k) {
			t.Fatalf("Valid(%q)", k)
		}
		c := For(k)
		if c == nil {
			continue
		}
		for _, v := range []string{c.Accent, c.Accent2, c.Soft, c.Highlight, c.Bg0, c.Bg1} {
			if !hex.MatchString(v) {
				t.Fatalf("%s: цвет %q не #rrggbb", k, v)
			}
		}
	}
	if Valid("url(javascript:alert(1))") {
		t.Fatal("Valid")
	}
	if sw := Swatch(Default); sw.Accent != "#8b5cf6" || sw.Accent2 != "#5eb7ff" || sw.Bg1 != "#071018" {
		t.Fatalf("свотч standard = %+v", sw)
	}
	if sw := Swatch("teal"); sw.Accent != "#0d9488" || sw.Bg1 != "#051c1c" {
		t.Fatalf("свотч teal = %+v", sw)
	}
}
