//go:build integration

package migratetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// LaravelFixture — значения, зашифрованные настоящим Encrypter старого Laravel в php:8.4.27-cli
// (internal/migrate/testdata/laravel_crypt.json, tools/migrate-from-mysql/laravel-crypt-fixture.sh).
type LaravelFixture struct {
	AppKey       string `json:"app_key"`
	PreviousKeys string `json:"previous_keys"`
	Cases        []struct {
		Name    string `json:"name"`
		Plain   string `json:"plain"`
		Payload string `json:"payload"`
	} `json:"cases"`
}

// Case — значение фикстуры по имени.
func (f LaravelFixture) Case(t testing.TB, name string) (plain, payload string) {
	t.Helper()
	for _, c := range f.Cases {
		if c.Name == name {
			return c.Plain, c.Payload
		}
	}
	t.Fatalf("no laravel fixture case %q", name)
	return "", ""
}

// LoadLaravelFixture читает фикстуру шифрования.
func LoadLaravelFixture(t testing.TB) LaravelFixture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "testdata", "laravel_crypt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f LaravelFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// M2Data — что положил InjectM2 (открытые значения — только для проверок теста).
type M2Data struct {
	Fixture       LaravelFixture
	WebhookSecret string // открытый секрет вебхука (до шифрования старым APP_KEY)
	WebhookID     int64
	TelegramToken string
	APIKey        string // ключ интегратора zfk_…
	MobileTokenID int64
	MobileToken   string // часть после «|»
	Storage       string // корень диска 'local' старого Laravel
	LogoSHA256    string
	SessionID     string
}

func exec(t testing.TB, my *sql.DB, q string, args ...any) sql.Result {
	t.Helper()
	res, err := my.ExecContext(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("mysql: %s: %v", q, err)
	}
	return res
}

func lastID(t testing.TB, res sql.Result) int64 {
	t.Helper()
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// PNG — картинка PNG w×h.
func PNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, x%h, color.RGBA{R: 200, G: 30, B: 90, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// InjectM2 — данные для расширений M2 в клубе 1 демо-базы (как их записал бы старый код): настройки v1, чек без
// product_id, продажи тарифа в журнале, идущая сессия, гостевой приход, ключ API, вебхук с секретом APP_KEY,
// токены Sanctum, логотип на диске, переводы и токен Telegram платформы. now — момент переноса.
func InjectM2(t testing.TB, my *sql.DB, now time.Time) M2Data {
	t.Helper()
	d := M2Data{Fixture: LoadLaravelFixture(t), SessionID: "5b8f8f8a-1d1e-4c55-9d7e-0a6b7e1f0001"}
	utc := func(dur time.Duration) string { return now.Add(dur).UTC().Format(time.DateTime) }

	// Настройки клуба: минуты строкой, флаг строкой, уровни v1 (Q-023, Q-003).
	exec(t, my, `UPDATE clubs SET settings = ? WHERE id = 1`,
		`{"club":{"session_idle_timeout":"30 сек","booking_interval":30,"agent_shop_enabled":"1"},`+
			`"finances":{"loyalty_enabled":true,"loyalty_levels":[{"spent":50000,"percent":10},{"spent":10000,"percent":5.5}],`+
			`"topup_bonus_enabled":true,"topup_bonus_levels":[{"amount":5000,"percent":3}]}}`)

	// Старый чек (до refund-ов: только имя) — Snickers однозначен, «Неизвестный» — нет (CQ-9). Смена 3 открыта.
	exec(t, my, `INSERT INTO cash_transactions (cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at, club_id)
		VALUES (3, 4, 'income', 900.00, 'Продажа POS', ?, ?, ?, 1)`,
		`{"items":[{"name":"Snickers","price":400,"total":800,"quantity":2},{"name":"Неизвестный","price":"100.50","total":100.5,"quantity":1}]}`,
		utc(-30*time.Minute), utc(-30*time.Minute))
	// Гостевая сессия за наличные (CSQ-2): приход и запись журнала cash.income с details.computers.
	res := exec(t, my, `INSERT INTO cash_transactions (cash_shift_id, user_id, type, amount, description, receipt_data, created_at, updated_at, club_id)
		VALUES (3, 4, 'income', 600.00, 'Гостевая сессия: тариф «1 час», ПК VIP-04 (наличные)', NULL, ?, ?, 1)`, utc(-20*time.Minute), utc(-20*time.Minute))
	guest := lastID(t, res)
	exec(t, my, `INSERT INTO activity_logs (event, description, user_id, subject_type, subject_id, details, ip_address, created_at, updated_at, club_id)
		VALUES ('cash.income', 'Гостевая сессия', 4, 'App\\Models\\CashTransaction', ?, ?, '127.0.0.1', ?, ?, 1)`,
		guest, `{"computers":"VIP-04","minutes":60,"amount":"600.00","payment_method":"наличные","tariff":"1 час"}`, utc(-20*time.Minute), utc(-20*time.Minute))

	// Продажи тарифа 1 в агенте (Q-007): три записи tariff.purchased; идущая сессия на ПК 2 (SV-3): тариф + поминутно.
	for i := range 3 {
		exec(t, my, `INSERT INTO activity_logs (event, description, user_id, subject_type, subject_id, details, ip_address, created_at, updated_at, club_id)
			VALUES ('tariff.purchased', 'Куплен тариф: 1 час', 6, 'App\\Models\\Tariff', 1, ?, '127.0.0.1', ?, ?, 1)`,
			`{"amount":"600.00","session_id":"old-`+string(rune('a'+i))+`","computer_id":1}`, utc(-48*time.Hour), utc(-48*time.Hour))
	}
	exec(t, my, `UPDATE club_computers SET current_session_id = ?, current_session_user_id = 6, session_guest = 0, session_ends_at = ? WHERE id = 2`,
		d.SessionID, utc(2*time.Hour))
	exec(t, my, `INSERT INTO session_purchases (club_id, session_id, user_id, kind, tariff_id, name, minutes, main_cents, bonus_cents, created_at, updated_at)
		VALUES (1, ?, 6, 'tariff', 1, '1 час', 60, 60000, 0, ?, ?), (1, ?, 6, 'minutes', NULL, 'VIP-зал', 30, 50000, 0, ?, ?)`,
		d.SessionID, utc(-10*time.Minute), utc(-10*time.Minute), d.SessionID, utc(-5*time.Minute), utc(-5*time.Minute))
	exec(t, my, `INSERT INTO activity_logs (event, description, user_id, subject_type, subject_id, details, ip_address, created_at, updated_at, club_id)
		VALUES ('tariff.purchased', 'Куплен тариф: 1 час', 6, 'App\\Models\\Tariff', 1, ?, '127.0.0.1', ?, ?, 1),
		       ('time.purchased', 'Куплено 30 мин.', 6, 'App\\Models\\ClubZone', 1, ?, '127.0.0.1', ?, ?, 1)`,
		`{"amount":"600.00","session_id":"`+d.SessionID+`","computer_id":2}`, utc(-10*time.Minute), utc(-10*time.Minute),
		`{"amount":"500.00","session_id":"`+d.SessionID+`","computer_id":2}`, utc(-5*time.Minute), utc(-5*time.Minute))

	// Ключ API владельца клуба; отозванный ключ, выпущенный пользователем 14 (не участник клуба 1 — аккаунт
	// переносится по ссылке M2, иначе FK api_keys.created_by).
	d.APIKey = "zfk_abcd1234_" + "Aa0Bb1Cc2Dd3Ee4Ff5Gg6Hh7Ii8Jj9Kk0Ll1Mm2N"
	sum := sha256.Sum256([]byte(d.APIKey))
	res = exec(t, my, `INSERT INTO api_keys (club_id, created_by, name, prefix, key_hash, scopes, created_at, updated_at)
		VALUES (1, 2, 'Интеграция 1С', 'zfk_abcd1234', ?, '["customers:read","sales:read"]', ?, ?)`, hex.EncodeToString(sum[:]), utc(-24*time.Hour), utc(-24*time.Hour))
	keyID := lastID(t, res)
	old := sha256.Sum256([]byte("revoked"))
	exec(t, my, `INSERT INTO api_keys (club_id, created_by, name, prefix, key_hash, scopes, revoked_at, revoked_by, created_at, updated_at)
		VALUES (1, 14, 'Старый ключ', 'zfk_zzzz9999', ?, '["customers:read"]', ?, 14, ?, ?)`, hex.EncodeToString(old[:]), utc(-48*time.Hour), utc(-72*time.Hour), utc(-48*time.Hour))
	exec(t, my, `INSERT INTO api_idempotency_keys (api_key_id, idempotency_key, request_hash, status, response, created_at)
		VALUES (?, 'k-1', ?, 201, '{"data":{}}', ?)`, keyID, hex.EncodeToString(sum[:]), utc(-time.Hour))

	// Вебхук: секрет зашифрован APP_KEY настоящим PHP (каст 'encrypted'); доставка в очереди.
	var payload string
	d.WebhookSecret, payload = d.Fixture.Case(t, "webhook_secret")
	res = exec(t, my, `INSERT INTO webhook_endpoints (club_id, created_by, url, secret, events, is_active, failure_count, created_at, updated_at)
		VALUES (1, 2, 'https://hooks.example.com/zafiro', ?, '["stock.changed"]', 1, 0, ?, ?)`, payload, utc(-24*time.Hour), utc(-24*time.Hour))
	d.WebhookID = lastID(t, res)
	exec(t, my, `INSERT INTO webhook_deliveries (club_id, webhook_endpoint_id, event_id, event, payload, status, attempts, created_at, updated_at)
		VALUES (1, ?, '0c4b7b0e-5e2f-4bd7-9a52-3d6b8f9b0001', 'stock.changed', '{"event":"stock.changed","data":{"quantity":5}}', 'delivered', 1, ?, ?)`,
		d.WebhookID, utc(-2*time.Hour), utc(-2*time.Hour))

	// Токены Sanctum: действующий клуба 1, истёкший, клуба 2, аккаунт без клуба 1 (переносится только первый).
	d.MobileToken = "plainTextMobileToken0123456789abcdefABCDEF"
	tsum := sha256.Sum256([]byte(d.MobileToken))
	res = exec(t, my, `INSERT INTO personal_access_tokens (tokenable_type, tokenable_id, name, token, abilities, expires_at, created_at, updated_at)
		VALUES ('App\\Models\\User', 9, 'mobileclient', ?, '["club:1"]', ?, ?, ?)`, hex.EncodeToString(tsum[:]), utc(29*24*time.Hour), utc(-time.Hour), utc(-time.Hour))
	d.MobileTokenID = lastID(t, res)
	for i, row := range []struct {
		user, ability, expires string
	}{{"9", "club:1", utc(-time.Hour)}, {"14", "club:2", utc(time.Hour)}, {"1", "club:10", utc(time.Hour)}} {
		h := sha256.Sum256([]byte{byte(i)})
		exec(t, my, `INSERT INTO personal_access_tokens (tokenable_type, tokenable_id, name, token, abilities, expires_at, created_at, updated_at)
			VALUES ('App\\Models\\User', ?, 'mobileclient', ?, ?, ?, ?, ?)`, row.user, hex.EncodeToString(h[:]), `["`+row.ability+`"]`, row.expires, utc(-time.Hour), utc(-time.Hour))
	}

	// Логотип клуба на диске 'local'; у новости 1 путь есть, файла нет (предупреждение).
	d.Storage = t.TempDir()
	logo := PNG(t, 64, 64)
	ls := sha256.Sum256(logo)
	d.LogoSHA256 = hex.EncodeToString(ls[:])
	if err := os.MkdirAll(filepath.Join(d.Storage, "club-logos", "1"), 0o750); err != nil {
		t.Fatal(err)
	}
	logoPath := "club-logos/1/" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCd.png"
	if err := os.WriteFile(filepath.Join(d.Storage, filepath.FromSlash(logoPath)), logo, 0o600); err != nil {
		t.Fatal(err)
	}
	exec(t, my, "UPDATE clubs SET logo_path = ? WHERE id = 1", logoPath)
	exec(t, my, "UPDATE news SET image_path = 'news/1/missingmissingmissingmissingmissing0001.jpg' WHERE id = 1")

	// Платформа: переводы (подпись способа оплаты на казахском), токен Telegram (Crypt::encryptString).
	exec(t, my, "INSERT INTO translations (language_id, `group`, `key`, key_hash, value, created_at, updated_at) VALUES "+
		"(2, '*', 'наличные', SHA1('*|наличные'), 'қолма-қол', ?, ?), (3, '*', 'наличные', SHA1('*|наличные'), 'cash', ?, ?), "+
		"(1, '*', 'Касса', SHA1('*|Касса'), 'Касса', ?, ?)",
		utc(-time.Hour), utc(-time.Hour), utc(-time.Hour), utc(-time.Hour), utc(-time.Hour), utc(-time.Hour))
	var token string
	d.TelegramToken, token = d.Fixture.Case(t, "telegram_token")
	exec(t, my, "INSERT INTO platform_settings (`key`, value, updated_by, created_at, updated_at) VALUES ('telegram_gateway', ?, 1, ?, ?)",
		`{"token":"`+token+`"}`, utc(-time.Hour), utc(-time.Hour))
	return d
}
