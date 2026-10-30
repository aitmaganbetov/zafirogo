package money

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Фикстура снята PHP 8.3.6 скриптом tools/php-oracle/gen-round-fixture.php (см. README там же).
const fixturePath = "testdata/php_round.json"

type oracleFixture struct {
	PHP                  string            `json:"php"`
	TotalRoundCases      int               `json:"total_round_cases"`
	TotalNaiveMismatches int               `json:"total_naive_mismatches"`
	Round                []json.RawMessage `json:"round"`
	IntCast              []json.RawMessage `json:"int_cast"`
	TengeFloat           []json.RawMessage `json:"tenge_float"`
	Strings              []json.RawMessage `json:"strings"`
	Cents                []json.RawMessage `json:"cents"`
}

func loadFixture(t *testing.T, path string) oracleFixture {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // G304: путь — константа теста или временный файл теста
	if err != nil {
		t.Fatalf("чтение фикстуры: %v", err)
	}
	var f oracleFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("разбор фикстуры: %v", err)
	}
	return f
}

func unrow(t *testing.T, raw json.RawMessage, dst ...any) {
	t.Helper()
	var row []json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("строка %s: %v", raw, err)
	}
	if len(row) != len(dst) {
		t.Fatalf("строка %s: ожидалось %d полей", raw, len(dst))
	}
	for i, d := range dst {
		if err := json.Unmarshal(row[i], d); err != nil {
			t.Fatalf("строка %s поле %d: %v", raw, i, err)
		}
	}
}

func f64(t *testing.T, h string) float64 {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 8 {
		t.Fatalf("hex %q", h)
	}
	var u uint64
	for _, c := range b {
		u = u<<8 | uint64(c)
	}
	return math.Float64frombits(u)
}

func hexBits(f float64) string {
	u := math.Float64bits(f)
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = byte(u)
		u >>= 8
	}
	return hex.EncodeToString(b[:])
}

func naiveRound(x float64, places int) float64 {
	f := math.Pow(10, float64(places))
	return math.Round(x*f) / f
}

// checkRoundRows сравнивает PHPRound с PHP побитно; возвращает число кейсов, где наивный math.Round ошибается.
func checkRoundRows(t *testing.T, rows []json.RawMessage) (naiveWrong int) {
	t.Helper()
	failures := 0
	for _, raw := range rows {
		var xs, rs, cat string
		var places int
		var naiveDiffers bool
		unrow(t, raw, &xs, &places, &rs, &cat, &naiveDiffers)
		x, want := f64(t, xs), f64(t, rs)
		got := PHPRound(x, places)
		if math.Float64bits(got) != math.Float64bits(want) {
			failures++
			if failures <= 20 {
				t.Errorf("PHPRound(%v [%s], %d) = %v [%s], PHP: %v [%s] (%s)", x, xs, places, got, hexBits(got), want, rs, cat)
			}
		}
		if places >= 0 && places <= 3 && math.Float64bits(naiveRound(x, places)) != math.Float64bits(want) {
			naiveWrong++
		}
	}
	if failures > 0 {
		t.Fatalf("расхождений с PHP: %d из %d", failures, len(rows))
	}
	return naiveWrong
}

func TestPHPRoundFixture(t *testing.T) {
	f := loadFixture(t, fixturePath)
	if len(f.Round) < 2000 {
		t.Fatalf("в фикстуре мало кейсов round(): %d", len(f.Round))
	}
	naive := checkRoundRows(t, f.Round)
	// Защита от «упрощения» PHPRound до math.Round: фикстура обязана содержать кейсы, где они расходятся.
	if naive < 1000 {
		t.Fatalf("наивный math.Round ошибся лишь в %d кейсах — фикстура не показательна", naive)
	}
	t.Logf("PHP %s: %d кейсов round() совпали побитно; наивный math.Round ошибся бы в %d", f.PHP, len(f.Round), naive)
}

// TestPHPRoundOracleFull — полный дифференциальный тест (≥ 200 000 кейсов) против живого PHP.
// Запускается только при ZAFIRO_PHP_ORACLE=1 и наличии php в PATH (CI без PHP его пропускает).
func TestPHPRoundOracleFull(t *testing.T) {
	if os.Getenv("ZAFIRO_PHP_ORACLE") != "1" {
		t.Skip("ZAFIRO_PHP_ORACLE != 1")
	}
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php не найден")
	}
	out := filepath.Join(t.TempDir(), "full.json")
	script := filepath.Join("..", "..", "..", "tools", "php-oracle", "gen-round-fixture.php")
	cmd := exec.Command(php, "-d", "memory_limit=2G", script, "--full", out) //nolint:gosec // G204: тестовый запуск оракула
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("php: %v", err)
	}
	f := loadFixture(t, out)
	if len(f.Round) < 200000 {
		t.Fatalf("оракул вернул %d кейсов, нужно ≥ 200 000", len(f.Round))
	}
	naive := checkRoundRows(t, f.Round)
	t.Logf("PHP %s: %d кейсов round() совпали побитно; наивный math.Round ошибся бы в %d", f.PHP, len(f.Round), naive)
}

func TestIntCastFixture(t *testing.T) {
	f := loadFixture(t, fixturePath)
	for _, raw := range f.IntCast {
		var xs, str string
		var want int64
		unrow(t, raw, &xs, &want, &str)
		x := f64(t, xs)
		if got := PHPIntCast(x); got != want {
			t.Errorf("PHPIntCast(%v) = %d, PHP (int): %d", x, got, want)
		}
		if got := PHPFloatString(x); got != str {
			t.Errorf("PHPFloatString(%v) = %q, PHP (string): %q", x, got, str)
		}
	}
}

func TestTengeFloatFixture(t *testing.T) {
	f := loadFixture(t, fixturePath)
	for _, raw := range f.TengeFloat {
		var xs, nf, str string
		var cents int64
		unrow(t, raw, &xs, &cents, &nf, &str)
		x := f64(t, xs)
		if got := FromTengeFloat(x); int64(got) != cents {
			t.Errorf("FromTengeFloat(%v) = %d, PHP (int) round(x*100): %d", x, got, cents)
		}
		if got := FormatFloat2(x); got != nf {
			t.Errorf("FormatFloat2(%v) = %q, PHP number_format: %q", x, got, nf)
		}
		if got := PHPFloatString(x); got != str {
			t.Errorf("PHPFloatString(%v) = %q, PHP (string): %q", x, got, str)
		}
	}
}

func TestStringsFixture(t *testing.T) {
	f := loadFixture(t, fixturePath)
	for _, raw := range f.Strings {
		var s, fs string
		var i, friends, stored int64
		var legacy *int64
		var numeric bool
		unrow(t, raw, &s, &i, &fs, &legacy, &friends, &stored, &numeric)
		if got := PHPStringToInt(s); got != i {
			t.Errorf("PHPStringToInt(%q) = %d, PHP: %d", s, got, i)
		}
		if got := PHPStringToFloat(s); math.Float64bits(got) != math.Float64bits(f64(t, fs)) {
			t.Errorf("PHPStringToFloat(%q) = %v, PHP: %v", s, got, f64(t, fs))
		}
		got, err := LegacyAmountToCents(s)
		switch {
		case legacy == nil && err == nil:
			t.Errorf("LegacyAmountToCents(%q) = %d, PHP: TypeError", s, got)
		case legacy != nil && (err != nil || got != *legacy):
			t.Errorf("LegacyAmountToCents(%q) = %d, %v; PHP: %d", s, got, err, *legacy)
		}
		if got := ParseTransferAmount(s); int64(got) != friends {
			t.Errorf("ParseTransferAmount(%q) = %d, PHP Friends::amountToCents: %d", s, got, friends)
		}
		if got := StoredCents(s); int64(got) != stored {
			t.Errorf("StoredCents(%q) = %d, PHP Friends::storedCents: %d", s, got, stored)
		}
		if got := PHPIsNumeric(s); got != numeric {
			t.Errorf("PHPIsNumeric(%q) = %v, PHP is_numeric: %v", s, got, numeric)
		}
	}
}

func TestCentsFixture(t *testing.T) {
	f := loadFixture(t, fixturePath)
	for _, raw := range f.Cents {
		var c int64
		var legacy, friends, nf string
		unrow(t, raw, &c, &legacy, &friends, &nf)
		if got := LegacyFormatCents(c); got != legacy {
			t.Errorf("LegacyFormatCents(%d) = %q, PHP: %q", c, got, legacy)
		}
		if friends != "" {
			if got := Tiyn(c).Tenge(); got != friends {
				t.Errorf("Tiyn(%d).Tenge() = %q, PHP Friends::formatCents: %q", c, got, friends)
			}
		}
		if Tiyn(c).Valid() {
			if got := Tiyn(c).Tenge(); got != nf {
				t.Errorf("Tiyn(%d).Tenge() = %q, PHP number_format(c/100): %q", c, got, nf)
			}
		}
	}
}
