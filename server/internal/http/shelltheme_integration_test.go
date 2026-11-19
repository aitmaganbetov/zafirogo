//go:build integration

package httpserver

import (
	"net/http"
	"testing"
	"time"
)

// Тема ПК клуба — настройки «Агент ПК» (старый tests/Feature/ShellThemeTest.php, коммит e38b764): по умолчанию
// standard, сохранение рассылает ConfigChanged агентам, допускаются только темы из списка.
func TestShellThemeSettings(t *testing.T) {
	e, g := newG5Env(t, time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC))
	_, owner := e.staff("Владелец", "owner")
	save := func(theme any) resp {
		return e.send("PUT", owner, "/api/settings/sections/club", map[string]any{
			"session_idle_timeout": 15, "pc_restart_after_session_end": 0.5, "session_auto_terminate_when_pc_unavailable": 5,
			"booking_interval": 30, "agent_shell_theme": theme,
		})
	}

	page := e.get(owner, "/api/settings")
	expect(t, page, http.StatusOK, "settings")
	if got := obj(obj(page.body["settings"])["club"])["agent_shell_theme"]; got != "standard" {
		t.Fatalf("по умолчанию = %v", got)
	}
	themes, _ := page.body["shell_themes"].([]any)
	if len(themes) != 7 || obj(themes[3])["key"] != "red" || obj(themes[3])["accent"] != "#dc2626" || obj(themes[0])["bg_1"] != "#071018" {
		t.Fatalf("shell_themes = %v", themes)
	}

	before := g.configCount()
	expect(t, save("red"), http.StatusOK, "save red")
	if got := e.clubSettings()["club"]["agent_shell_theme"]; got != "red" {
		t.Fatalf("сохранено = %v", got)
	}
	if g.configCount() != before+1 {
		t.Fatal("ConfigChanged не разослан")
	}
	if d := e.activityDetails("settings.updated"); d["agent_shell_theme"] != "standard → red" {
		t.Fatalf("журнал = %v", d)
	}
	if got := obj(obj(e.get(owner, "/api/settings").body["settings"])["club"])["agent_shell_theme"]; got != "red" {
		t.Fatalf("после сохранения = %v", got)
	}

	expectField(t, save("url(javascript:alert(1))"), "agent_shell_theme", "The selected agent shell theme is invalid.")
	if got := e.clubSettings()["club"]["agent_shell_theme"]; got != "red" {
		t.Fatalf("неверная тема сохранилась: %v", got)
	}
}
