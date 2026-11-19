//go:build integration

package guestapi_test

import (
	"context"
	"testing"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
)

// Тема ПК клуба (старый tests/Feature/ShellThemeTest.php, коммит e38b764): пока клуб не выбрал тему, агент остаётся
// в своей (shell_theme не задан); выбранная тема приходит цветами в Welcome и push-ом ConfigChanged без
// переподключения; неизвестное значение в настройках — стандартная тема.
func TestShellThemeInClubConfig(t *testing.T) {
	h := newHarness(t, harnessOpts{noLimits: true})
	p := h.enrollPC()
	as := p.connect("boot-1", 0)
	if th := as.welcome.GetConfig().GetShellTheme(); th != nil {
		t.Fatalf("тема до выбора = %+v", th)
	}

	ctx := context.Background()
	id := identity.Identity{ClubID: h.club, ComputerID: p.id}
	h.exec(`UPDATE clubs SET settings = '{"club": {"agent_shell_theme": "red"}}' WHERE id = $1`, h.club)
	cmd, err := h.srv.Hooks().ConfigChanged(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.stream.Commands().Send(ctx, h.club, p.id, cmd); err != nil {
		t.Fatal(err)
	}
	got := as.waitCommand(func(c *agentv1.Command) bool { return c.GetConfigChanged() != nil })
	th := got.GetConfigChanged().GetConfig().GetShellTheme()
	want := &agentv1.ShellTheme{Accent: "#dc2626", Accent_2: "#fb7185", Soft: "#fca5a5", Highlight: "#fda4af", Bg_0: "#0a0304", Bg_1: "#1f0709"}
	if th.GetAccent() != want.GetAccent() || th.GetAccent_2() != want.GetAccent_2() || th.GetSoft() != want.GetSoft() ||
		th.GetHighlight() != want.GetHighlight() || th.GetBg_0() != want.GetBg_0() || th.GetBg_1() != want.GetBg_1() {
		t.Fatalf("красная тема = %+v", th)
	}

	// Новое подключение (перезапуск агента) — тема в Welcome.
	if th := p.connect("boot-1", 0).welcome.GetConfig().GetShellTheme(); th.GetBg_0() != "#0a0304" {
		t.Fatalf("тема в Welcome = %+v", th)
	}

	for _, settings := range []string{
		`{"club": {"agent_shell_theme": "standard"}}`,
		`{"club": {"agent_shell_theme": null}}`,
		`{"club": {"agent_shell_theme": "url(javascript:alert(1))"}}`,
		`{"club": {"agent_shell_theme": ["red"]}}`,
	} {
		h.exec(`UPDATE clubs SET settings = $2::jsonb WHERE id = $1`, h.club, settings)
		cmd, err := h.srv.Hooks().ConfigChanged(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if th := cmd.GetConfigChanged().GetConfig().GetShellTheme(); th != nil {
			t.Fatalf("%s: тема = %+v, ожидалась стандартная", settings, th)
		}
	}
}
