package reseller

import "testing"

// Reseller::normalizeCode(), Reseller::CODE_PATTERN (GS-560: «Arman-Games» → «arman-games»; GS-562: « Arman-Games »).
func TestCode(t *testing.T) {
	for _, c := range []struct {
		in, want string
		valid    bool
	}{
		{"Arman-Games", "arman-games", true},
		{" Arman-Games ", "arman-games", true},
		{"abc", "abc", true},
		{"a1-", "a1-", true},
		{"0ab", "0ab", true},
		{"ab", "ab", false},                   // короче 3
		{"-abc", "-abc", false},               // начинается с дефиса
		{"arman games", "arman games", false}, // пробел внутри
		{"арман", "арман", false},             // не латиница
		{"a_b_c", "a_b_c", false},
		{"", "", false},
		{"a23456789012345678901234567890-2", "a23456789012345678901234567890-2", true},    // 32 знака
		{"a23456789012345678901234567890-23", "a23456789012345678901234567890-23", false}, // 33 знака
	} {
		got := NormalizeCode(c.in)
		if got != c.want || ValidCode(got) != c.valid {
			t.Errorf("%q: %q valid=%v, ожидалось %q valid=%v", c.in, got, ValidCode(got), c.want, c.valid)
		}
	}
}

// Superadmin\ResellerController::store(): телефон формы (GS-560: «8 700 123-45-67» и «7001234567» → +77001234567).
func TestNormalizePhone(t *testing.T) {
	for _, c := range []struct {
		in, want string
		valid    bool
	}{
		{"8 700 123-45-67", "+77001234567", true},
		{"7001234567", "+77001234567", true},
		{"+77005556677", "+77005556677", true},
		{"77005556677", "+77005556677", true},
		{"+7 (700) 555-66-77", "+77005556677", true},
		{"", "", false},
		{"abc", "", false},
		{"12345", "+712345", false},
		{"9 700 123-45-67", "+797001234567", false}, // 11 цифр не с 7 и не с 8 — не обрезается
	} {
		got := NormalizePhone(c.in)
		if got != c.want || ValidPhone(got) != c.valid {
			t.Errorf("%q: %q valid=%v, ожидалось %q valid=%v", c.in, got, ValidPhone(got), c.want, c.valid)
		}
	}
}

// BR-187, GAP-053: «к выплате» — разность в тиынах, может быть отрицательной (выплата больше остатка не запрещена).
func TestBalance(t *testing.T) {
	for _, c := range []struct {
		name            string
		earned, paidOut int64
		want            int64
	}{
		{"GS-563 480 ₸ заработано, 300 ₸ выплачено", 48000, 30000, 18000},
		{"ничего не было", 0, 0, 0},
		{"всё выплачено", 59404, 59404, 0},
		{"GAP-053 выплата больше остатка", 18000, 50000, -32000},
		{"тиыны", 31493, 1, 31492},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Balance(c.earned, c.paidOut); got != c.want {
				t.Fatalf("Balance(%d, %d) = %d, ожидалось %d", c.earned, c.paidOut, got, c.want)
			}
		})
	}
}

// Процент без хвостовых нулей (rtrim(number_format(…, 2)) в reseller/dashboard.blade.php).
func TestFormatPercent(t *testing.T) {
	for bp, want := range map[int]string{0: "0", 1000: "10", 1250: "12.5", 1235: "12.35", 10000: "100", 5: "0.05", 50: "0.5", 1005: "10.05"} {
		if got := FormatPercent(bp); got != want {
			t.Errorf("FormatPercent(%d) = %q, ожидалось %q", bp, got, want)
		}
	}
}

func TestFormatTenge(t *testing.T) {
	for tiyn, want := range map[int64]string{0: "0", 30000: "300", 59404: "594.04", 59450: "594.5", 1: "0.01", -32000: "-320", 99_999_999_900: "999999999"} {
		if got := FormatTenge(tiyn); got != want {
			t.Errorf("FormatTenge(%d) = %q, ожидалось %q", tiyn, got, want)
		}
	}
}
