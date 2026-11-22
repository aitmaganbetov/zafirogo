package httpx

import "testing"

// Адрес страницы кабинета для оповещений — только из конфигурации: на хосте кабинета — путь с корня, иначе
// <адрес сайта>/superadmin/…; без того и другого ссылки нет (ревью 22.11.2026: раньше хост брался из заголовка Host).
func TestCabinetURL(t *testing.T) {
	for _, tc := range []struct{ domain, public, path, want string }{
		{"superadmin.zafiro.kz", "https://zafiro.kz", "/clubs/7", "https://superadmin.zafiro.kz/clubs/7"},
		{"superadmin.zafiro.kz", "", "/invoices", "https://superadmin.zafiro.kz/invoices"},
		{"", "https://zafiro.kz", "/clubs/7", "https://zafiro.kz/superadmin/clubs/7"},
		{"", "http://localhost:8080", "/invoices", "http://localhost:8080/superadmin/invoices"},
		{"", "", "/clubs/7", ""},
	} {
		if got := (SuperadminDeps{Domain: tc.domain, PublicURL: tc.public}).CabinetURL(tc.path); got != tc.want {
			t.Errorf("domain %q, public %q: %q, want %q", tc.domain, tc.public, got, tc.want)
		}
	}
}
