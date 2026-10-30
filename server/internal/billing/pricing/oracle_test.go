//go:build phporacle

package pricing

// Дифференциальный тест со старым PHP-кодом через tools/php-oracle/eval.php (README там же).
//
//	cd server && go test -tags=phporacle -run TestPHPOracle -v ./internal/billing/pricing/
//
// Команда оракула — ZAFIRO_ORACLE_CMD (читает JSONL со stdin, пишет JSONL в stdout); по умолчанию
// tools/php-oracle/run.sh eval (копия Zafiro, vendor из ~/.cache/zafirogo-php-oracle; локальный PHP < 8.4 —
// Docker, образ PHP_IMAGE, например mirror.gcr.io/library/php:8.4-cli). Последний прогон (2026-10-30, PHP 8.4.27):
// 27 500 вызовов, расхождений 0, ~7 мин (money.PHPRound — PHP 8.4). С уже готовой копией:
//
//	ZAFIRO_ORACLE_CMD='docker run --rm -i --network none -v <копия>:/app -v <repo>/tools/php-oracle:/oracle:ro \
//	  -w /app mirror.gcr.io/library/php:8.4-cli php /oracle/eval.php --app /app'
//
// Входы случайные, но детерминированные (ZAFIRO_ORACLE_SEED, по умолчанию 20261030); число — ZAFIRO_ORACLE_N
// (множитель, по умолчанию 1 ≈ 27 000 вызовов).

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

type oracleCall struct {
	ID   int            `json:"id"`
	Fn   string         `json:"fn"`
	Args map[string]any `json:"args"`
	// check сравнивает ответ PHP с Go; возвращает описание расхождения или "".
	check func(res oracleResult) string `json:"-"`
}

type oracleResult struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Type    string              `json:"type"`
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	} `json:"error"`
}

func TestPHPOracle(t *testing.T) {
	seed := uint64(20261030)
	if s := os.Getenv("ZAFIRO_ORACLE_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	mult := 1
	if s := os.Getenv("ZAFIRO_ORACLE_N"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 {
			t.Fatalf("ZAFIRO_ORACLE_N=%q", s)
		}
		mult = v
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	var calls []*oracleCall
	add := func(c *oracleCall) { c.ID = len(calls); calls = append(calls, c) }
	for range 10000 * mult {
		add(weeklyContainsCall(rng))
	}
	for range 1000 * mult {
		add(weeklyIsAlwaysCall(rng))
	}
	for range 7000 * mult {
		add(paymentRulesCall(rng))
	}
	for range 3000 * mult {
		add(bonusLimitCall(rng))
	}
	for range 3000 * mult {
		add(discountedCall(rng))
	}
	for range 2000 * mult {
		add(minutesPriceCall(rng))
	}
	for range 1500 * mult {
		add(purchaseCall(rng))
	}

	results := runOracle(t, calls)
	byFn := map[string][2]int{} // всего, расхождений
	var hard []string
	for i, c := range calls {
		st := byFn[c.Fn]
		st[0]++
		if diff := c.check(results[i]); diff != "" {
			argsJSON, _ := json.Marshal(c.Args)
			st[1]++
			hard = append(hard, fmt.Sprintf("%s %s: %s", c.Fn, argsJSON, diff))
		}
		byFn[c.Fn] = st
	}
	total := 0
	for fn, st := range byFn {
		total += st[0]
		t.Logf("%-45s вызовов %6d, расхождений %d", fn, st[0], st[1])
	}
	t.Logf("всего вызовов: %d (seed %d)", total, seed)
	for i, h := range hard {
		if i >= 30 {
			t.Errorf("… и ещё %d", len(hard)-30)
			break
		}
		t.Error(h)
	}
}

func runOracle(t *testing.T, calls []*oracleCall) []oracleResult {
	t.Helper()
	cmdline := os.Getenv("ZAFIRO_ORACLE_CMD")
	if cmdline == "" {
		_, file, _, _ := runtime.Caller(0)
		cmdline = filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "tools", "php-oracle", "run.sh") + " eval"
	}
	cmd := exec.Command("sh", "-c", cmdline) // #nosec G204 -- команда оракула задаёт разработчик (тест под тегом phporacle)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		w := bufio.NewWriter(stdin)
		enc := json.NewEncoder(w)
		for _, c := range calls {
			if err := enc.Encode(c); err != nil {
				panic(err)
			}
		}
		_ = w.Flush()
		_ = stdin.Close()
	}()
	results := make([]oracleResult, 0, len(calls))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r oracleResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("ответ оракула не JSON: %q", sc.Text())
		}
		results = append(results, r)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("оракул: %v", err)
	}
	if len(results) != len(calls) {
		t.Fatalf("оракул вернул %d ответов из %d", len(results), len(calls))
	}
	for i, r := range results {
		if r.ID != calls[i].ID {
			t.Fatalf("ответ %d: id %d", i, r.ID)
		}
	}
	t.Logf("оракул: %d вызовов за %s", len(calls), time.Since(start).Round(time.Millisecond))
	return results
}

// ── генераторы входов ──

func pick[T any](rng *rand.Rand, xs ...T) T { return xs[rng.IntN(len(xs))] }

func randDay(rng *rand.Rand) any {
	if rng.IntN(12) == 0 {
		return pick[any](rng, "xyz", "", "Mon", " mon", "mon\n", 1, nil, true, "sunday")
	}
	return pick[any](rng, "mon", "tue", "wed", "thu", "fri", "sat", "sun")
}

func randTime(rng *rand.Rand) any {
	switch rng.IntN(14) {
	case 0:
		return pick[any](rng, "9:00", "24:60", "99:99", "12:30\n", "12:30\n\n", " 12:30", "１２:００", "12-30", "", "12:30:00", 1200, nil, 12.5, true)
	case 1:
		return pick[any](rng, "00:00", "23:59", "06:00", "22:00", "12:00")
	default:
		return fmt.Sprintf("%02d:%02d", rng.IntN(24), rng.IntN(60))
	}
}

func randPeriods(rng *rand.Rand) (raw any, parsed WeeklyPeriods) {
	switch rng.IntN(20) {
	case 0:
		return nil, nil
	case 1:
		return []any{}, WeeklyPeriods{}
	}
	n := 1 + rng.IntN(3)
	rows := make([]any, 0, n)
	for range n {
		row := map[string]any{}
		if rng.IntN(15) == 0 {
			row["start_day"], row["start_time"], row["end_day"], row["end_time"] = "mon", "00:00", "sun", "23:59"
		} else {
			for _, k := range []string{"start_day", "end_day"} {
				if rng.IntN(40) != 0 { // иногда ключа нет
					row[k] = randDay(rng)
				}
			}
			for _, k := range []string{"start_time", "end_time"} {
				if rng.IntN(40) != 0 {
					row[k] = randTime(rng)
				}
			}
		}
		rows = append(rows, row)
	}
	b, _ := json.Marshal(rows)
	p, err := ParseWeeklyPeriods(b)
	if err != nil {
		panic(err)
	}
	return rows, p
}

func weeklyContainsCall(rng *rand.Rand) *oracleCall {
	raw, p := randPeriods(rng)
	// Минута недели 2026-10-26 (пн) … + 3 недели, секунды — иногда.
	base := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	at := base.Add(time.Duration(rng.IntN(3*7*1440)) * time.Minute)
	if rng.IntN(4) == 0 {
		at = at.Add(time.Duration(rng.IntN(60)) * time.Second)
	}
	// Часто — ровно на границе периода (начало/конец ±1 минута).
	if len(p) > 0 && rng.IntN(2) == 0 {
		per := p[rng.IntN(len(p))]
		if d, ok := weekDays[pick(rng, per.StartDay, per.EndDay)]; ok {
			m := d*1440 + periodMinutes(pick(rng, per.StartTime, per.EndTime)) + rng.IntN(3) - 1
			m = (m + 7*1440) % (7 * 1440)
			at = base.Add(time.Duration(7*1440*rng.IntN(3)+m) * time.Minute).Add(time.Duration(rng.IntN(2)*59) * time.Second)
		}
	}
	want := p.Contains(at)
	return &oracleCall{Fn: "WeeklyPeriods.contains", Args: map[string]any{"periods": raw, "at": at.Format("2006-01-02 15:04:05")},
		check: func(r oracleResult) string { return cmpJSON(r, want) }}
}

func weeklyIsAlwaysCall(rng *rand.Rand) *oracleCall {
	raw, p := randPeriods(rng)
	want := p.IsAlways()
	return &oracleCall{Fn: "WeeklyPeriods.isAlways", Args: map[string]any{"periods": raw},
		check: func(r oracleResult) string { return cmpJSON(r, want) }}
}

// randSettingValue — значение настройки любого типа, как оно может лежать в JSON (сырой текст JSON).
func randSettingValue(rng *rand.Rand, kind string) json.RawMessage {
	var s string
	switch rng.IntN(10) {
	case 0, 1, 2, 3: // «нормальное» значение
		switch kind {
		case "int":
			s = strconv.Itoa(rng.IntN(140) - 20)
		case "bool":
			s = pick(rng, "true", "false")
		case "scope":
			s = pick(rng, `"tariffs"`, `"hourly"`, `"all"`)
		}
	case 4:
		s = pick(rng, `"1"`, `"0"`, `""`, `"false"`, `"true"`, `"on"`, `" 50 "`, `"30.7"`, `"1e1"`, `"12abc"`, `"abc"`, `".5"`, `"-0"`, `" 7"`, `"7 "`, `"0x1A"`, `"1e400"`, `"99999999999999999999"`)
	case 5:
		s = pick(rng, "0", "1", "-1", "0.0", "-0.0", "30.7", "99.999", "100.5", "1e1", "1e30", "-1e30", "99999999999999999999", "9223372036854775807", "-9223372036854775808", "60.9", "5.5")
	case 6:
		s = pick(rng, "null", "[]", "{}", "[1]", `{"a":1}`, `["all"]`)
	case 7:
		s = pick(rng, `"tariffs"`, `"hourly"`, `"all"`, `"ALL"`, `"never"`, `" all"`, `""`)
	case 8:
		s = fmt.Sprintf("%d.%d", rng.IntN(120)-10, rng.IntN(100))
	default:
		return nil // ключа нет
	}
	return json.RawMessage(s)
}

func randFinances(rng *rand.Rand) map[string]any {
	f := map[string]any{}
	for key, kind := range map[string]string{
		"discount_scope": "scope", "bonus_max_percent": "int", "allow_mixed_payment": "bool", "allow_whole_balance": "bool",
		"auto_billing": "bool", "auto_billing_pause_minutes": "int",
	} {
		if v := randSettingValue(rng, kind); v != nil {
			f[key] = v
		}
	}
	return f
}

func rulesOf(f map[string]any) PaymentRules {
	b, err := json.Marshal(map[string]any{"finances": f})
	if err != nil {
		panic(err)
	}
	fin, err := ParseFinances(b)
	if err != nil {
		panic(err)
	}
	return RulesFromFinances(fin)
}

func paymentRulesCall(rng *rand.Rand) *oracleCall {
	f := randFinances(rng)
	r := rulesOf(f)
	want := map[string]any{
		"discountScope": string(r.DiscountScope), "discountOnTariffs": r.DiscountOnTariffs(), "discountOnHourly": r.DiscountOnHourly(),
		"bonusMaxPercent": r.BonusMaxPercent, "autoBillingEnabled": r.AutoBilling, "autoBillingPauseMinutes": r.AutoBillingPauseMinutes,
		"wholeBalanceAllowed": r.WholeBalance, "mixedPaymentAllowed": r.MixedPayment,
		"bonusPercentOffered": r.BonusPercentOffered(false), "bonusPercentOfferedMainOnly": r.BonusPercentOffered(true),
	}
	return &oracleCall{Fn: "PaymentRules.all", Args: map[string]any{"finances": f}, check: func(res oracleResult) string { return cmpJSON(res, want) }}
}

func randPrice(rng *rand.Rand) money.Tiyn {
	switch rng.IntN(6) {
	case 0:
		return money.Tiyn(rng.Int64N(int64(MaxPrice) + 1))
	case 1:
		return money.Tiyn(rng.IntN(200))
	case 2:
		return pick[money.Tiyn](rng, 0, 1, 99, 100, 99999, 100000, MaxPrice, MaxPrice-1)
	default:
		return money.Tiyn(rng.IntN(500000) * pick(rng, 1, 1, 100))
	}
}

func bonusLimitCall(rng *rand.Rand) *oracleCall {
	f := map[string]any{"bonus_max_percent": json.RawMessage(strconv.Itoa(rng.IntN(101)))}
	if v := randSettingValue(rng, "int"); v != nil && rng.IntN(3) == 0 {
		f["bonus_max_percent"] = v
	}
	price := randPrice(rng)
	want := rulesOf(f).BonusLimit(price)
	return &oracleCall{Fn: "PaymentRules.bonusLimitCents", Args: map[string]any{"finances": f, "price": int64(price)},
		check: func(res oracleResult) string { return cmpJSON(res, int64(want)) }}
}

func discountedCall(rng *rand.Rand) *oracleCall {
	price := randPrice(rng)
	var percent float64
	switch rng.IntN(5) {
	case 0: // три знака, часто x.xx5: здесь PHP 8.3 и 8.4 расходятся (money.PHPRound — 8.4, stage1-money MQ-1)
		percent = float64(rng.IntN(100001)) / 1000
	case 1:
		percent = pick(rng, 0, 100, 150, -5, 12.5, 33.333, 7.5, 0.01, 99.99, 100.01, 0.145, 1.005, 40.245, 77.835)
	default: // decimal(5,2) — как в club_memberships.discount и уровнях лояльности
		percent = float64(rng.IntN(10001)) / 100
	}
	want, err := Discounted(price, percent)
	if err != nil {
		panic(err)
	}
	return &oracleCall{Fn: "AgentTariffPurchaseService.discounted", Args: map[string]any{"price": int64(price), "percent": percent},
		check: func(res oracleResult) string { return cmpJSON(res, int64(want)) }}
}

func minutesPriceCall(rng *rand.Rand) *oracleCall {
	hour := money.Tiyn(rng.IntN(100000001)) // до 1 000 000.00 (StoreClubZoneRequest)
	if rng.IntN(4) == 0 {
		hour = money.Tiyn(rng.IntN(1000))
	}
	minutes := int64(rng.IntN(721))
	want, err := MinutesPrice(hour, minutes)
	if err != nil {
		panic(err)
	}
	return &oracleCall{Fn: "AgentTariffPurchaseService.minutesPriceInCents", Args: map[string]any{"price_per_hour": hour.Tenge(), "minutes": minutes},
		check: func(res oracleResult) string { return cmpJSON(res, int64(want)) }}
}

func randBalance(rng *rand.Rand, price money.Tiyn) money.Tiyn {
	switch rng.IntN(5) {
	case 0:
		return 0
	case 1:
		return price
	case 2:
		return money.Tiyn(rng.Int64N(int64(price)*2 + 2))
	case 3:
		// Отрицательных балансов не генерируем: старый amountToCents("-0.01") читает их как +1 тиын
		// (BR-001, Q-004), а в новой схеме баланс — точный bigint. См. stage1-pricing.md PQ-8.
		return max(0, price/2+money.Tiyn(rng.IntN(3))-1)
	default:
		return money.Tiyn(rng.IntN(5000000))
	}
}

func purchaseCall(rng *rand.Rand) *oracleCall {
	f := map[string]any{
		"bonus_max_percent":   pick(rng, 0, 30, 50, 99, 100, rng.IntN(101)),
		"allow_mixed_payment": rng.IntN(2) == 0,
		"discount_scope":      pick(rng, "tariffs", "hourly", "all"),
	}
	rules := RulesFromFinances(RawFinances{"bonus_max_percent": f["bonus_max_percent"], "allow_mixed_payment": f["allow_mixed_payment"], "discount_scope": f["discount_scope"]})
	discount := money.Tiyn(pick(rng, 0, 0, 500, 750, 1000, 1250, 3333, 10000, rng.IntN(10001))) // сотые доли процента, decimal(5,2)
	discountPercent, _ := strconv.ParseFloat(discount.Tenge(), 64)
	choice := pick(rng, ChoiceNone, ChoiceMain, ChoiceBonus)
	args := map[string]any{"finances": f, "discount": discount.Tenge()}
	if choice != ChoiceNone {
		args["balance"] = string(choice)
	}

	var (
		q   Purchase
		err error
	)
	var price money.Tiyn
	if rng.IntN(3) == 0 {
		hour := pick[money.Tiyn](rng, 60000, 50000, 55500, 100, money.Tiyn(1+rng.IntN(500000)))
		minutes := int64(1 + rng.IntN(720))
		hourPrice, _ := HourlyPrice(hour, rules, discountPercent)
		price, _ = MinutesPrice(hourPrice, minutes)
		b := Balances{randBalance(rng, price), randBalance(rng, price)}
		args["kind"], args["price_per_hour"], args["minutes"] = "minutes", hour.Tenge(), minutes
		args["main"], args["bonus"] = b.Main.Tenge(), b.Bonus.Tenge()
		q, err = QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: &hour, Rules: rules, DiscountPercent: discountPercent,
			UserID: 1, Minutes: minutes, Choice: choice, Balances: b, Now: time.Now()})
	} else {
		tariff := Tariff{
			Price: pick[money.Tiyn](rng, 100000, 99999, 0, 1, 10000, money.Tiyn(rng.IntN(1000000))), DurationMinutes: int64(1 + rng.IntN(600)),
			PaymentMethod: pick(rng, MainOrBonus, MainOrBonus, MainBalanceOnly), DiscountEnabled: rng.IntN(3) != 0, IsActive: true,
		}
		price, _ = TariffPrice(tariff, rules, discountPercent)
		b := Balances{randBalance(rng, price), randBalance(rng, price)}
		args["kind"], args["price"], args["duration_minutes"] = "tariff", tariff.Price.Tenge(), tariff.DurationMinutes
		args["payment_method"], args["discount_enabled"] = string(tariff.PaymentMethod), tariff.DiscountEnabled
		args["main"], args["bonus"] = b.Main.Tenge(), b.Bonus.Tenge()
		q, err = QuoteTariffPurchase(TariffPurchaseInput{Tariff: tariff, Rules: rules, DiscountPercent: discountPercent, Role: "client",
			UserID: 1, Choice: choice, Balances: b, Now: time.Now()})
	}
	return &oracleCall{Fn: "Purchase.run", Args: args, check: func(res oracleResult) string {
		if !res.OK {
			return fmt.Sprintf("оракул: %+v", res.Error)
		}
		var got struct {
			OK       bool                `json:"ok"`
			Response map[string]any      `json:"response"`
			Errors   map[string][]string `json:"errors"`
			Part     *struct {
				Main    int64 `json:"main_cents"`
				Bonus   int64 `json:"bonus_cents"`
				Minutes int64 `json:"minutes"`
			} `json:"part"`
		}
		if e := json.Unmarshal(res.Result, &got); e != nil {
			return e.Error()
		}
		if err != nil {
			var pe *Error
			if !asErr(err, &pe) {
				return "Go: " + err.Error()
			}
			msgs := got.Errors[pe.Field]
			if got.OK || len(msgs) != 1 || msgs[0] != strings.TrimPrefix(pe.Error(), pe.Field+": ") {
				return fmt.Sprintf("Go: ошибка %q, PHP: ok=%v %v %v", pe.Error(), got.OK, got.Errors, got.Response)
			}
			return ""
		}
		if !got.OK || got.Part == nil {
			return fmt.Sprintf("Go: %+v (цена %d), PHP: ошибка %v", q.Payment, price, got.Errors)
		}
		wantResp := fmt.Sprintf("%s/%s/%d", q.Payment.After.Main.Tenge(), q.Payment.After.Bonus.Tenge(), q.Minutes)
		gotResp := fmt.Sprintf("%v/%v/%v", got.Response["new_balance"], got.Response["new_bonus"], got.Response["minutes_added"])
		if wantResp != gotResp || got.Part.Main != int64(q.Payment.Main) || got.Part.Bonus != int64(q.Payment.Bonus) || got.Part.Minutes != q.Minutes {
			return fmt.Sprintf("Go %s main %d bonus %d; PHP %s part %+v", wantResp, q.Payment.Main, q.Payment.Bonus, gotResp, *got.Part)
		}
		return ""
	}}
}

func asErr(err error, target **Error) bool { return errors.As(err, target) }

// cmpJSON сравнивает result оракула с want после нормализации через JSON (числа — как float64 из JSON).
func cmpJSON(r oracleResult, want any) string {
	if !r.OK {
		return fmt.Sprintf("оракул: %+v", r.Error)
	}
	wb, _ := json.Marshal(want)
	var w, g any
	_ = json.Unmarshal(wb, &w)
	if err := json.Unmarshal(r.Result, &g); err != nil {
		return err.Error()
	}
	if fmt.Sprint(w) != fmt.Sprint(g) {
		return fmt.Sprintf("Go %s, PHP %s", wb, r.Result)
	}
	return ""
}
