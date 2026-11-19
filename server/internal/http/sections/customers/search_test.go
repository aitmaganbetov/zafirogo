package customers

import "testing"

// Laravel: app/Http/Controllers/UserController.php UserController::index() (35c36a0) — телефон по цифрам.
func TestPhonePattern(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"8 700 123-45-67", "%77001234567%"},
		{"+7 (700) 123 45 67", "%77001234567%"},
		{"87001234567", "%77001234567%"},
		{"8700123456", "%8700123456%"}, // 10 цифр — ведущая 8 остаётся
		{"999 88", "%99988%"},
		{"Иван 12", ""}, // меньше трёх цифр — телефон не ищется
		{"+7", ""},
		{"", ""},
		{"a1b2c3", "%123%"},
		{"٣٤٥", ""}, // \D без /u — только ASCII-цифры
	} {
		in := c.in
		if got := phonePattern(&in); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	if phonePattern(nil) != "" {
		t.Error("nil")
	}
}
