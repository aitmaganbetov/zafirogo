package auth

import "testing"

// route('superadmin.clubs.show', $club): на хосте кабинета — /clubs/{id}, иначе /superadmin/clubs/{id}.
func TestCabinetClubURL(t *testing.T) {
	if got := cabinetClubURL("superadmin.zafiro.kz", "zafiro.kz", 7); got != "https://superadmin.zafiro.kz/clubs/7" {
		t.Fatal(got)
	}
	if got := cabinetClubURL("", "zafiro.kz", 7); got != "https://zafiro.kz/superadmin/clubs/7" {
		t.Fatal(got)
	}
}
