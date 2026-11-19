// Package shelltheme — темы оформления ПК клуба (оболочка гостя и экран входа агента), настройки клуба →
// «Агент ПК», поле club.agent_shell_theme.
//
// Laravel: app/Support/ShellThemes.php (коммит старого проекта e38b764). Агенту уходят сами цвета (ClubConfig.shell_theme),
// поэтому новая тема здесь не требует выпуска агента. «standard» не отправляет ничего: агент остаётся в своей теме.
package shelltheme

// Default — ShellThemes::DEFAULT.
const Default = "standard"

// Colors — цвета темы. accent — кнопки и активный пункт меню (белый текст на нём); accent_2 — выделение и кнопка
// тарифа (тёмный текст); soft — подзаголовки и ссылки; highlight — магазин (тёмный текст); bg_0 и bg_1 — тёмный
// фон сверху вниз.
type Colors struct {
	Accent    string
	Accent2   string
	Soft      string
	Highlight string
	Bg0       string
	Bg1       string
}

// keys — порядок тем на странице настроек (ShellThemes::COLORS / names()).
var keys = []string{"standard", "blue", "green", "red", "orange", "teal", "pink"}

// colors — Laravel: app/Support/ShellThemes.php ShellThemes::COLORS (standard — null).
var colors = map[string]*Colors{
	"standard": nil,
	"blue":     {Accent: "#2563eb", Accent2: "#38bdf8", Soft: "#93c5fd", Highlight: "#7dd3fc", Bg0: "#03060c", Bg1: "#071226"},
	"green":    {Accent: "#16a34a", Accent2: "#4ade80", Soft: "#86efac", Highlight: "#bef264", Bg0: "#030805", Bg1: "#061a0e"},
	"red":      {Accent: "#dc2626", Accent2: "#fb7185", Soft: "#fca5a5", Highlight: "#fda4af", Bg0: "#0a0304", Bg1: "#1f0709"},
	"orange":   {Accent: "#ea580c", Accent2: "#fbbf24", Soft: "#fdba74", Highlight: "#fcd34d", Bg0: "#0a0603", Bg1: "#1f1005"},
	"teal":     {Accent: "#0d9488", Accent2: "#2dd4bf", Soft: "#5eead4", Highlight: "#99f6e4", Bg0: "#020909", Bg1: "#051c1c"},
	"pink":     {Accent: "#db2777", Accent2: "#f472b6", Soft: "#f9a8d4", Highlight: "#fbcfe8", Bg0: "#0a0308", Bg1: "#1f0716"},
}

// Keys — ShellThemes::keys(): допустимые значения настройки (Rule::in в SaveSettingsRequest).
func Keys() []string { return append([]string(nil), keys...) }

// Valid — ключ из списка тем.
func Valid(key string) bool {
	_, ok := colors[key]
	return ok
}

// For — ShellThemes::colors(): цвета для агента; nil — стандартная тема агента (standard или неизвестный ключ;
// значение не строкой вызывающий передаёт пустой строкой).
func For(key string) *Colors {
	c := colors[key]
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// standardSwatch — свотч стандартной темы на странице настроек: цвета самого агента (ShellThemes::swatch()).
var standardSwatch = Colors{Accent: "#8b5cf6", Accent2: "#5eb7ff", Bg1: "#071018"}

// Swatch — ShellThemes::swatch(): цвета свотча на странице настроек; стандартная (и неизвестная) — цвета агента.
func Swatch(key string) Colors {
	if c := colors[key]; c != nil {
		return *c
	}
	return standardSwatch
}
