//go:build phporacle

// Дифференциальный тест пакета bonus против старого PHP-кода (go test -tags=phporacle).
//
//	ZAFIRO_ORACLE_APP=<копия Zafiro с vendor/> go test -tags=phporacle -run TestPHPOracle -v ./internal/billing/bonus/
//
// Переменные окружения:
//
//	ZAFIRO_ORACLE_APP    копия старого проекта с установленным vendor/ (tools/php-oracle/run.sh shell печатает её путь)
//	ZAFIRO_ORACLE_MODE   direct (по умолчанию) — testdata/oracle_direct.php на локальном PHP 8.3: настоящие классы
//	                     PercentLevels/TopUpBonus/LoyaltyProgram без поднятия Laravel; eval — tools/php-oracle/eval.php
//	                     (полный Laravel, PHP >= 8.4: локальный или Docker-образ ZAFIRO_ORACLE_IMAGE)
//	ZAFIRO_ORACLE_N      число случайных входов (по умолчанию 25 000)
//	ZAFIRO_ORACLE_SEED   зерно генератора (по умолчанию 20261030)
//	ZAFIRO_ORACLE_PHP    PHP CLI (по умолчанию php)
//	ZAFIRO_ORACLE_IMAGE  образ для режима eval, если локальный PHP < 8.4 (по умолчанию mirror.gcr.io/library/php:8.4.27-cli)
//
// Режим eval идёт на PHP 8.4, а money.PHPRound повторяет 8.3 (docs/open-questions/stage1-money.md MQ-1):
// у сумм с полутиыном (x.xx5) (int) round($amount * 100) в 8.3 и 8.4 расходится, поэтому в этом режиме
// суммы пополнения генерируются только с ≤ 2 знаками после точки.
package bonus

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

type oracleCall struct {
	ID   int            `json:"id"`
	Fn   string         `json:"fn"`
	Args map[string]any `json:"args"`
}

type oracleResult struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// oracleCase — вызов PHP и проверка его ответа Go-реализацией (nil — совпало, иначе описание расхождения).
type oracleCase struct {
	kind  string
	call  oracleCall
	check func(r oracleResult) error
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestPHPOracle(t *testing.T) {
	app := os.Getenv("ZAFIRO_ORACLE_APP")
	if app == "" {
		t.Skip("ZAFIRO_ORACLE_APP не задан: нужна копия Zafiro с vendor/ (tools/php-oracle/run.sh shell)")
	}
	mode := envOr("ZAFIRO_ORACLE_MODE", "direct")
	n, _ := strconv.Atoi(envOr("ZAFIRO_ORACLE_N", "25000"))
	seed, _ := strconv.ParseUint(envOr("ZAFIRO_ORACLE_SEED", "20261030"), 10, 64)
	g := &gen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), eval: mode == "eval"}

	cases := make([]oracleCase, 0, n+1)
	for i := 0; i < n; i++ {
		var c oracleCase
		switch p := g.r.IntN(100); {
		case p < 25:
			c = g.normalizeCase()
		case p < 70:
			c = g.topUpCase()
		case p < 90 || g.eval:
			c = g.loyaltyCase()
		default:
			c = g.levelsCase()
		}
		c.call.ID = i
		cases = append(cases, c)
	}

	var in bytes.Buffer
	enc := json.NewEncoder(&in)
	enc.SetEscapeHTML(false)
	versionFn := "version"
	if g.eval {
		versionFn = "PHP.version"
	}
	if err := enc.Encode(oracleCall{ID: -1, Fn: versionFn, Args: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if err := enc.Encode(c.call); err != nil {
			t.Fatal(err)
		}
	}

	cmd := oracleCommand(t, mode, app)
	cmd.Stdin = &in
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("оракул: %v\n%s", err, tail(stderr.String(), 2000))
	}

	results := map[int]oracleResult{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	phpVersion := "?"
	for sc.Scan() {
		var r oracleResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("ответ оракула не JSON: %v: %s", err, tail(sc.Text(), 300))
		}
		if r.ID == -1 {
			phpVersion = string(r.Result)
			continue
		}
		results[r.ID] = r
	}
	if g.eval && strings.HasPrefix(strings.Trim(phpVersion, `"`), "8.3") {
		t.Logf("внимание: режим eval на PHP %s", phpVersion)
	}

	perKind := map[string]int{}
	mismatches := map[string]int{}
	shown := 0
	for _, c := range cases {
		perKind[c.kind]++
		r, ok := results[c.call.ID]
		if !ok {
			t.Fatalf("нет ответа на вызов %d (%s)", c.call.ID, c.kind)
		}
		if err := c.check(r); err != nil {
			mismatches[c.kind]++
			if shown < 25 {
				shown++
				args, _ := json.Marshal(c.call.Args)
				t.Errorf("%s #%d: %v\n  args: %s\n  php: ok=%v result=%s error=%s", c.kind, c.call.ID, err, tail(string(args), 600), r.OK, r.Result, r.Error)
			}
		}
	}
	t.Logf("PHP %s, режим %s, seed %d: %d вызовов %v; расхождений %v", phpVersion, mode, seed, len(cases), perKind, mismatches)
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func oracleCommand(t *testing.T, mode, app string) *exec.Cmd {
	t.Helper()
	php := envOr("ZAFIRO_ORACLE_PHP", "php")
	switch mode {
	case "direct":
		script, err := filepath.Abs("testdata/oracle_direct.php")
		if err != nil {
			t.Fatal(err)
		}
		return exec.Command(php, script, app) // #nosec G204 -- тест разработчика, пути из окружения
	case "eval":
		oracleDir, err := filepath.Abs("../../../../tools/php-oracle")
		if err != nil {
			t.Fatal(err)
		}
		if v, err := exec.Command(php, "-r", "echo PHP_VERSION_ID;").Output(); err == nil { // #nosec G204
			if id, _ := strconv.Atoi(string(v)); id >= 80400 {
				return exec.Command(php, filepath.Join(oracleDir, "eval.php"), "--app", app) // #nosec G204
			}
		}
		image := envOr("ZAFIRO_ORACLE_IMAGE", "mirror.gcr.io/library/php:8.4.27-cli")
		return exec.Command("docker", "run", "--rm", "-i", "--network", "none", // #nosec G204
			"-v", app+":/app", "-v", oracleDir+":/oracle:ro", "-w", "/app", image, "php", "/oracle/eval.php", "--app", "/app")
	}
	t.Fatalf("неизвестный ZAFIRO_ORACLE_MODE=%q", mode)
	return nil
}

// ── генератор входов ──

type gen struct {
	r    *rand.Rand
	eval bool // eval.php на PHP 8.4: без полутиынов в суммах и без INF (json_encode не сохранит настройки)
}

func (g *gen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

// cents2 — случайная сумма в тиынах, лог-равномерно от 1 до limit.
func (g *gen) cents2(limit float64) int64 {
	return int64(math.Exp(g.r.Float64() * math.Log(limit)))
}

func tenge(c int64) string { return money.Tiyn(c).Tenge() }

// numLit — JSON-литерал числа или строки-числа для значения v (тиыны или сотые процента), с разными формами записи.
func (g *gen) numLit(hundredths int64) string {
	s := tenge(hundredths)
	whole := strings.TrimSuffix(strings.TrimSuffix(s, "00"), ".")
	switch g.r.IntN(8) {
	case 0:
		return whole // 1000 или 7.5 как JSON-число
	case 1:
		return s // 7.50 — JSON-число с нулями
	case 2:
		return `"` + s + `"` // "7.50" — как из формы
	case 3:
		return `"` + whole + `"`
	case 4: // лишние знаки: округление round(x, 2)
		extra := strconv.Itoa(g.r.IntN(1000))
		if g.r.IntN(3) == 0 {
			extra = "5"
		}
		if !strings.Contains(s, ".") {
			return s + "." + extra
		}
		return s + extra
	case 5:
		return `" ` + s + ` "`
	case 6:
		return strconv.FormatFloat(float64(hundredths)/100, 'e', -1, 64) // 1e3, 7.5e0
	default:
		return s
	}
}

// junkLit — значения, которые normalize() должен отбросить или прочитать по правилам PHP.
func (g *gen) junkLit() string {
	xs := []string{`null`, `true`, `false`, `""`, `"abc"`, `"1,5"`, `"0x1A"`, `"1e3"`, `".5"`, `"5."`, `"-3"`, `[]`, `[1]`, `{"a":1}`,
		`"12abc"`, `" "`, `-0`, `-0.0`, `0`, `"0"`, `-5`, `99999999999999999999`, `9223372036854775807`, `"9223372036854775808"`, `1.005`, `12.345`, `2.675`, `0.145`}
	if !g.eval {
		xs = append(xs, `1e400`, `"1e400"`, `-1e400`)
	}
	return g.pick(xs...)
}

// levelsLit — JSON уровней. realistic — корректные уровни (для расчётов); иначе — со «шумом».
func (g *gen) levelsLit(key string, realistic bool) (string, []int64) {
	n := g.r.IntN(21)
	if !realistic && g.r.IntN(10) == 0 {
		n = 20 + g.r.IntN(6)
	}
	thresholds := make([]int64, 0, n)
	items := make([]string, 0, n)
	for i := 0; i < n; i++ {
		th := g.cents2(99_999_999_900)
		if g.r.IntN(4) == 0 {
			th = (th / 100) * 100 // целые тенге
		}
		if g.r.IntN(10) == 0 && len(thresholds) > 0 {
			th = thresholds[g.r.IntN(len(thresholds))] // равные пороги — стабильность сортировки
		}
		pc := 1 + g.r.Int64N(10000)
		if g.r.IntN(3) == 0 {
			pc = (pc / 100) * 100
		}
		thresholds = append(thresholds, th)
		thLit, pcLit := g.numLit(th), g.numLit(pc)
		if !realistic {
			if g.r.IntN(6) == 0 {
				thLit = g.junkLit()
			}
			if g.r.IntN(6) == 0 {
				pcLit = g.junkLit()
			}
		}
		item := `{"` + key + `":` + thLit + `,"percent":` + pcLit + `}`
		if g.r.IntN(2) == 0 {
			item = `{"percent":` + pcLit + `,"` + key + `":` + thLit + `}`
		}
		if !realistic {
			switch g.r.IntN(12) {
			case 0:
				item = `{"percent":` + pcLit + `}`
			case 1:
				item = g.junkLit()
			case 2:
				item = `[` + thLit + `,` + pcLit + `]`
			case 3:
				item = `{"` + key + `":` + thLit + `,"percent":` + pcLit + `,"x":1}`
			case 4:
				item = `{"` + key + `":` + thLit + `,"percent":` + pcLit + `,"` + key + `":` + g.numLit(g.cents2(1e6)) + `}`
			}
		}
		items = append(items, item)
	}
	if !realistic && g.r.IntN(8) == 0 { // объект вместо списка
		pairs := make([]string, len(items))
		for i, it := range items {
			pairs[i] = `"k` + strconv.Itoa(g.r.IntN(1000)) + `":` + it
		}
		return "{" + strings.Join(pairs, ",") + "}", thresholds
	}
	if !realistic && g.r.IntN(20) == 0 {
		return g.junkLit(), nil
	}
	return "[" + strings.Join(items, ",") + "]", thresholds
}

// flagLit — значение *_enabled: (bool) по правилам PHP.
func (g *gen) flagLit() string {
	if g.r.IntN(2) == 0 {
		return "true"
	}
	xs := []string{`false`, `1`, `0`, `"1"`, `"0"`, `""`, `"false"`, `"yes"`, `[]`, `[0]`, `0.0`, `0.1`}
	if !g.eval {
		xs = append(xs, `null`) // eval.php: null ?? true — подменился бы на true
	}
	return g.pick(xs...)
}

// amountLit — сумма пополнения строкой (как $data['amount']), часто на пороге ± 1 тиын.
func (g *gen) amountLit(thresholds []int64) string {
	c := g.cents2(999_999_999_999)
	if len(thresholds) > 0 && g.r.IntN(3) == 0 {
		c = thresholds[g.r.IntN(len(thresholds))] + int64(g.r.IntN(3)) - 1
		if c < 1 {
			c = 1
		}
	}
	s := tenge(c)
	if !g.eval && g.r.IntN(5) == 0 { // полутиын и лишние знаки: (int) round($amount * 100) PHP 8.3
		s += g.pick("5", "49", "51", "05", "95", "4999999", "5000001")
	}
	return s
}

func (g *gen) normalizeCase() oracleCase {
	key := g.pick(ThresholdAmount, ThresholdSpent)
	if g.r.IntN(10) == 0 {
		key = g.pick("amount", "spent") // ключ уровней не совпадает с порогом — всё отбрасывается
	}
	levels, _ := g.levelsLit(g.pick(ThresholdAmount, ThresholdSpent, key, key, key), false)
	threshold := key
	c := oracleCase{kind: "normalize"}
	if g.eval {
		c.call = oracleCall{Fn: "PercentLevels.normalize", Args: map[string]any{"levels": json.RawMessage(levels), "threshold": threshold}}
	} else {
		c.call = oracleCall{Fn: "normalize", Args: map[string]any{"levels": levels, "threshold": threshold}}
	}
	c.check = func(r oracleResult) error {
		v, err := DecodeJSON([]byte(levels))
		if err != nil {
			return err
		}
		return compareLevels(r, Normalize(v, threshold), threshold)
	}
	return c
}

func (g *gen) topUpCase() oracleCase {
	realistic := g.r.IntN(5) != 0
	levels, thresholds := g.levelsLit(ThresholdAmount, realistic)
	flag := g.flagLit()
	amount := g.amountLit(thresholds)
	settings := `{"finances":{"topup_bonus_enabled":` + flag + `,"topup_bonus_levels":` + levels + `}}`
	c := oracleCase{kind: "topup"}
	if g.eval {
		c.call = oracleCall{Fn: "TopUpBonus.for", Args: map[string]any{"amount": amount, "levels": json.RawMessage(levels), "enabled": json.RawMessage(flag)}}
	} else {
		c.call = oracleCall{Fn: "topup", Args: map[string]any{"settings": settings, "amount": amount}}
	}
	c.check = func(r oracleResult) error {
		s, err := ParseSettings([]byte(settings))
		if err != nil {
			return err
		}
		got, err := TopUpForTengeString(s.TopUpLevelsInForce(), amount)
		if err != nil {
			if !r.OK {
				return nil
			}
			return fmt.Errorf("Go: %w, PHP: ok", err)
		}
		if !r.OK {
			return fmt.Errorf("PHP ошибка, Go %+v", got)
		}
		var php struct {
			Percent json.Number `json:"percent"`
			Cents   json.Number `json:"cents"`
		}
		if err := decodeUseNumber(r.Result, &php); err != nil {
			return err
		}
		if err := sameFloat(php.Percent, got.Percent); err != nil {
			return fmt.Errorf("percent: %w", err)
		}
		if php.Cents.String() != strconv.FormatInt(int64(got.Cents), 10) {
			return fmt.Errorf("cents: PHP %s, Go %d", php.Cents, got.Cents)
		}
		return nil
	}
	return c
}

func (g *gen) loyaltyCase() oracleCase {
	levels, thresholds := g.levelsLit(ThresholdSpent, g.r.IntN(5) != 0)
	flag := g.flagLit()
	spent := g.cents2(999_999_999_999)
	if len(thresholds) > 0 && g.r.IntN(3) == 0 {
		spent = max(0, thresholds[g.r.IntN(len(thresholds))]+int64(g.r.IntN(3))-1)
	}
	if g.r.IntN(20) == 0 {
		spent = 0
	}
	personal := g.r.Int64N(10001)
	if g.r.IntN(3) == 0 {
		personal = 0
	}
	totalSpent, discount := tenge(spent), tenge(personal) // decimal:2 — строки "1234.50"
	settings := `{"finances":{"loyalty_enabled":` + flag + `,"loyalty_levels":` + levels + `}}`
	c := oracleCase{kind: "loyalty"}
	if g.eval {
		c.call = oracleCall{Fn: "LoyaltyProgram.discountFor", Args: map[string]any{"levels": json.RawMessage(levels), "enabled": json.RawMessage(flag), "total_spent": totalSpent, "discount": discount}}
	} else {
		c.call = oracleCall{Fn: "loyalty", Args: map[string]any{"settings": settings, "total_spent": totalSpent, "discount": discount}}
	}
	c.check = func(r oracleResult) error {
		s, err := ParseSettings([]byte(settings))
		if err != nil {
			return err
		}
		if !r.OK {
			return fmt.Errorf("PHP ошибка")
		}
		levels := s.LoyaltyLevelsInForce()
		gotDiscount := LoyaltyDiscountFor(levels, float64(personal)/100, money.Tiyn(spent))
		if g.eval {
			var php json.Number
			if err := decodeUseNumber(r.Result, &php); err != nil {
				return err
			}
			return sameFloat(php, gotDiscount)
		}
		var php struct {
			Percent  json.Number `json:"percent"`
			Discount json.Number `json:"discount"`
		}
		if err := decodeUseNumber(r.Result, &php); err != nil {
			return err
		}
		if err := sameFloat(php.Percent, LoyaltyPercentFor(levels, money.Tiyn(spent))); err != nil {
			return fmt.Errorf("percentFor: %w", err)
		}
		if err := sameFloat(php.Discount, gotDiscount); err != nil {
			return fmt.Errorf("discountFor: %w", err)
		}
		return nil
	}
	return c
}

// levelsCase — разбор clubs.settings целиком (только direct): уровни в силе для обеих программ.
func (g *gen) levelsCase() oracleCase {
	topup, _ := g.levelsLit(ThresholdAmount, false)
	loyalty, _ := g.levelsLit(ThresholdSpent, false)
	finances := []string{}
	if g.r.IntN(5) != 0 {
		finances = append(finances, `"topup_bonus_enabled":`+g.flagLit())
	}
	if g.r.IntN(5) != 0 {
		finances = append(finances, `"topup_bonus_levels":`+topup)
	}
	if g.r.IntN(5) != 0 {
		finances = append(finances, `"loyalty_enabled":`+g.flagLit())
	}
	if g.r.IntN(5) != 0 {
		finances = append(finances, `"loyalty_levels":`+loyalty)
	}
	settings := `{"club":{"booking_interval":30},"finances":{` + strings.Join(finances, ",") + `}}`
	switch g.r.IntN(15) {
	case 0:
		settings = `{"finances":` + g.junkLit() + `}`
	case 1:
		settings = g.pick(`null`, `[]`, `{}`, `{"finances":`, `5`, `"x"`, ``)
	}
	c := oracleCase{kind: "levels", call: oracleCall{Fn: "levels", Args: map[string]any{"settings": settings}}}
	c.check = func(r oracleResult) error {
		s, err := ParseSettings([]byte(settings))
		if err != nil || !r.OK {
			if err != nil && !r.OK {
				return nil
			}
			return fmt.Errorf("Go err=%w, PHP ok=%v", err, r.OK)
		}
		var php struct {
			TopUp   json.RawMessage `json:"topup"`
			Loyalty json.RawMessage `json:"loyalty"`
		}
		if err := json.Unmarshal(r.Result, &php); err != nil {
			return err
		}
		if err := compareLevels(oracleResult{OK: true, Result: php.TopUp}, s.TopUpLevelsInForce(), ThresholdAmount); err != nil {
			return fmt.Errorf("topup: %w", err)
		}
		if err := compareLevels(oracleResult{OK: true, Result: php.Loyalty}, s.LoyaltyLevelsInForce(), ThresholdSpent); err != nil {
			return fmt.Errorf("loyalty: %w", err)
		}
		return nil
	}
	return c
}

// ── сравнение ──

func decodeUseNumber(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

func phpNumber(n json.Number) (Number, error) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		return FloatNumber(f), err
	}
	i, err := strconv.ParseInt(s, 10, 64)
	return IntNumber(i), err
}

func sameNumber(php json.Number, got Number) error {
	want, err := phpNumber(php)
	if err != nil {
		return err
	}
	if want.IsFloat() != got.IsFloat() || want.Int() != got.Int() || math.Float64bits(want.Float()) != math.Float64bits(got.Float()) {
		return fmt.Errorf("PHP %s, Go %s (float=%v)", php, got, got.IsFloat())
	}
	return nil
}

func sameFloat(php json.Number, got float64) error {
	f, err := strconv.ParseFloat(php.String(), 64)
	if err != nil {
		return err
	}
	if math.Float64bits(f) != math.Float64bits(got) {
		return fmt.Errorf("PHP %s, Go %v", php, got)
	}
	return nil
}

func compareLevels(r oracleResult, got []Level, threshold string) error {
	if !r.OK {
		return fmt.Errorf("PHP ошибка")
	}
	var php []map[string]json.Number
	if err := decodeUseNumber(r.Result, &php); err != nil {
		return err
	}
	if len(php) != len(got) {
		return fmt.Errorf("уровней: PHP %d, Go %d", len(php), len(got))
	}
	for i := range php {
		if err := sameNumber(php[i][threshold], got[i].Threshold); err != nil {
			return fmt.Errorf("[%d].%s: %w", i, threshold, err)
		}
		if err := sameNumber(php[i]["percent"], got[i].Percent); err != nil {
			return fmt.Errorf("[%d].percent: %w", i, err)
		}
	}
	return nil
}
