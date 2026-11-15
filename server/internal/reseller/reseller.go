// Package reseller — реселлеры платформы: аккаунт с кодом, который клуб один раз вводит в настройках. Клуб реселлера
// платит подписку со скидкой реселлера (берётся большая из своей и реселлера), а реселлеру с каждой оплаты
// начисляется процент; выплаты записывает суперадмин. Реселлеров создаёт и удаляет суперадмин.
//
// Laravel: app/Models/Reseller.php, app/Models/ResellerPayout.php, app/Http/Controllers/Superadmin/ResellerController.php,
// app/Http/Controllers/ResellerController.php, SettingsController::reseller() (docs/business-rules.md BR-185…BR-189).
//
// Расчёт скидки и начисления — в internal/subscription (Discount, ResellerEarning): счёт получает снимок реселлера при
// выставлении, начисление и строку reseller_accrual:<id счёта> журнала денег платформы — при оплате. Здесь — учёт:
// создание, условия, привязка клуба, выплаты (строка reseller_payout:<id выплаты>), удаление и кабинет реселлера.
//
// Деньги — int64 в тиынах, процент реселлера — базисные пункты (1235 = 12,35 %); float нет (решение UQ-7). Все
// функции с q выполняются в транзакции пула zafiro_platform: таблицы реселлеров и привязку клуба пул клуба не меняет
// (миграция 0034).
package reseller

import (
	"regexp"
	"strconv"
	"strings"
)

// Пределы полей форм. Laravel: Superadmin\ResellerController::store(), update(), payout().
const (
	// MaxName — 'name' => max:120.
	MaxName = 120
	// MinPassword — 'password' => min:6.
	MinPassword = 6
	// MaxCode — длина кода (CodePattern: 3…32).
	MaxCode = 32
	// MaxCommissionBP — 'commission_percent' => max:100 (в базисных пунктах).
	MaxCommissionBP = 10000
	// MaxDiscountPercent — 'discount_percent' => max:100.
	MaxDiscountPercent = 100
	// MinPayout, MaxPayout — 'amount' => min:0.01, max:999999999 (в тиынах).
	MinPayout = 1
	MaxPayout = 999_999_999 * 100
	// MaxNote — 'note' => max:255.
	MaxNote = 255
)

// Размеры страниц. Laravel: Superadmin\ResellerController::index() — paginate(30); ResellerController::dashboard() —
// клубы по 30, начисления по 20, 20 последних выплат.
const (
	ListPage     = 30
	ClubsPage    = 30
	EarningsPage = 20
)

// codeRe — Reseller::CODE_PATTERN: латинские буквы, цифры и дефис, 3…32 знака, начинается с буквы или цифры.
var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,31}$`)

var phoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// NormalizeCode — коды сравниваются в нижнем регистре: «Arman» и «arman» — один код.
//
//	return mb_strtolower(trim((string) $code));
//
// Laravel: app/Models/Reseller.php Reseller::normalizeCode().
func NormalizeCode(code string) string { return strings.ToLower(strings.TrimSpace(code)) }

// ValidCode — код подходит под Reseller::CODE_PATTERN (после NormalizeCode).
func ValidCode(code string) bool { return codeRe.MatchString(code) }

// NormalizePhone — телефон формы «Новый реселлер»: только цифры; 11 цифр с ведущей 7 или 8 — без неё; «+7» + цифры.
// Пусто — "".
//
//	$digits = preg_replace('/\D+/', '', (string) $request->input('phone', ''));
//	$digits = strlen($digits) === 11 && in_array($digits[0], ['7', '8'], true) ? substr($digits, 1) : $digits;
//	'phone' => $digits === '' ? '' : '+7'.$digits
//
// Laravel: app/Http/Controllers/Superadmin/ResellerController.php ResellerController::store().
func NormalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if d == "" {
		return ""
	}
	if len(d) == 11 && (d[0] == '7' || d[0] == '8') {
		d = d[1:]
	}
	return "+7" + d
}

// ValidPhone — 'regex:/^\+7\d{10}$/' (после NormalizePhone).
func ValidPhone(phone string) bool { return phoneRe.MatchString(phone) }

// Balance — «к выплате»: заработано минус выплачено, тиыны. Превышение выплат не запрещено — остаток может быть
// отрицательным (BR-187, BR-188, GAP-053).
//
//	$balance = (float) $reseller->earned - (float) $reseller->paid_out;
//
// Laravel: resources/views/superadmin/resellers/index.blade.php, app/Http/Controllers/ResellerController.php
// ResellerController::dashboard().
func Balance(earned, paidOut int64) int64 { return earned - paidOut }

// FormatPercent — процент реселлера для текста без хвостовых нулей: 1250 → «12.5», 1000 → «10», 1235 → «12.35».
//
//	rtrim(rtrim(number_format((float) $reseller->commission_percent, 2, '.', ''), '0'), '.')
//
// Laravel: resources/views/reseller/dashboard.blade.php.
func FormatPercent(bp int) string {
	s := strconv.Itoa(bp/100) + "." + strconv.Itoa(bp%100/10) + strconv.Itoa(bp%10)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

// FormatTenge — сумма в тиынах как число тенге без хвостовых нулей (детали журнала платформы и текст выплаты):
// 30000 → «300», 59404 → «594.04», 59450 → «594.5».
func FormatTenge(tiyn int64) string {
	sign := ""
	if tiyn < 0 {
		sign, tiyn = "-", -tiyn
	}
	s := strconv.FormatInt(tiyn/100, 10) + "." + strconv.FormatInt(tiyn%100/10, 10) + strconv.FormatInt(tiyn%10, 10)
	return sign + strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
