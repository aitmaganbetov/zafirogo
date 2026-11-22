package password

import "testing"

// Аудит SEC-8: список частых паролей для владельцев и сотрудников клуба.
func TestIsCommon(t *testing.T) {
	for _, pw := range []string{"123456", "QWERTY123", "Password", "йцукен", "1q2w3e4r", "aaaaaa", "1111111", "ЯЯЯЯЯЯ", "zafiro123"} {
		if !IsCommon(pw) {
			t.Errorf("%q must be common", pw)
		}
	}
	for _, pw := range []string{"", "secret1", "Kj8#mq2v", "клуб-на-абая-12", "1234565", "aaaaab"} {
		if IsCommon(pw) {
			t.Errorf("%q must not be common", pw)
		}
	}
}
