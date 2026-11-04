package i18n

import "testing"

// MessageSelector::choose() Laravel: явные условия, формы по правилу языка, нет формы — первая.
func TestChoose(t *testing.T) {
	ru := "Уведомление появится на экране (:count ПК).|Уведомление появится на экранах (:count ПК).|много (:count ПК)."
	cases := []struct {
		locale, line string
		n            float64
		want         string
	}{
		{"ru", ru, 1, "Уведомление появится на экране (:count ПК)."},
		{"ru", ru, 3, "Уведомление появится на экранах (:count ПК)."},
		{"ru", ru, 11, "много (:count ПК)."},
		{"ru", ru, 21, "Уведомление появится на экране (:count ПК)."},
		{"kk", "бір|көп", 5, "көп"},
		{"kk", "тек", 5, "тек"},
		{"en", "{0} none|[1,1] one|[2,*] many", 0, "none"},
		{"en", "{0} none|[1,1] one|[2,*] many", 7, "many"},
		{"ru", "a|b", 5, "a"}, // формы с номером 2 нет — первая (MessageSelector::choose())
	}
	for _, c := range cases {
		if got := Choose(c.locale, c.line, c.n); got != c.want {
			t.Errorf("Choose(%q, %q, %v) = %q, want %q", c.locale, c.line, c.n, got, c.want)
		}
	}
}

func TestRender(t *testing.T) {
	got := Render("ru", "На :count ПК агент устарел (нужна версия :version).|На :count ПК агенты устарели (нужна версия :version).", map[string]string{"count": "3", "version": "2.1"})
	if want := "На 3 ПК агенты устарели (нужна версия 2.1)."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// длинные ключи раньше коротких (makeReplacements)
	if got := Render("ru", ":product / :productName", map[string]string{"product": "A", "productName": "B"}); got != "A / B" {
		t.Fatalf("got %q", got)
	}
}
