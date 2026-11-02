//go:build integration

package verifycode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/db/dbtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

const gatewayToken = "AAGexampleGatewayToken_1234567890"

var (
	tdb *dbtest.DB
	box *secretbox.Box
	pw  *password.Hasher
	// Настройка Gateway общая (platform_settings): тесты пакета не параллельные.
	seq atomic.Int64
)

func TestMain(m *testing.M) {
	d, cleanup, err := dbtest.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tdb = d
	if box, err = secretbox.New(strings.Repeat("k", 32)); err != nil {
		panic(err)
	}
	if pw, err = password.New(password.Options{Params: password.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}}); err != nil {
		panic(err)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time         { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) travel(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	t         *testing.T
	tg        *telegramtest.Server
	clock     *clock
	gateway   *telegram.Gateway
	recovery  *verifycode.PasswordRecovery
	transfer  *verifycode.TransferCodes
	signedOut []int64
}

// newEnv — Gateway подключён (withGateway()), поддельный Telegram (fakeGateway()).
func newEnv(t *testing.T, connected bool) *env {
	t.Helper()
	e := &env{t: t, tg: telegramtest.New(t), clock: &clock{t: time.Now().UTC().Truncate(time.Second)}}
	exec(t, "DELETE FROM platform_settings WHERE key = $1", telegram.SettingGateway)
	if connected {
		sealed, err := box.Seal(gatewayToken)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"token": sealed})
		exec(t, "INSERT INTO platform_settings (key, value, created_at, updated_at) VALUES ($1, $2, now(), now())", telegram.SettingGateway, raw)
	}
	e.gateway = &telegram.Gateway{API: &telegram.GatewayClient{URL: e.tg.GatewayURL()}, Store: &telegram.Store{Pool: tdb.Platform, Box: box}}
	e.recovery = &verifycode.PasswordRecovery{Platform: tdb.Platform, Gateway: e.gateway, Hasher: pw, Now: e.clock.Now,
		SignOut: func(_ context.Context, id int64) { e.signedOut = append(e.signedOut, id) }}
	e.transfer = &verifycode.TransferCodes{App: tdb.App, Platform: tdb.Platform, Gateway: e.gateway, Hasher: pw, Now: e.clock.Now}
	return e
}

func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := tdb.Owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tdb.Owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func phone() string {
	return fmt.Sprintf("+7709%07d", (time.Now().UnixNano()/1000+seq.Add(1)*7919)%10_000_000)
}

// user — аккаунт с паролем old-secret (Argon2id).
func user(t *testing.T, ph string, blocked bool) int64 {
	t.Helper()
	hash, err := pw.Hash(context.Background(), "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := tdb.Owner.QueryRow(context.Background(),
		"INSERT INTO users (name, phone, password, blocked_at, created_at, updated_at) VALUES ('Арман', $1, $2, CASE WHEN $3 THEN now() END, now(), now()) RETURNING id",
		ph, hash, blocked).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func kind(err error) verifycode.Kind {
	var ve *verifycode.Error
	if errors.As(err, &ve) {
		return ve.Kind
	}
	return 0
}

func passwordIs(t *testing.T, id int64, plain string) bool {
	t.Helper()
	var hash string
	if err := tdb.Owner.QueryRow(context.Background(), "SELECT password FROM users WHERE id = $1", id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	_, err := pw.Verify(context.Background(), plain, hash)
	return err == nil
}

// GS-236: код в Telegram (номер E.164, ttl 300, токен Bearer), хранится только хэш; верный код — новый пароль, выход
// везде, коды удалены, журнал платформы; код работает один раз.
func TestPasswordRecovery_AgentFlowSignsOutEverywhere(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	ph := phone()
	id := user(t, ph, false)

	if err := e.recovery.SendCode(ctx, verifycode.NormalizePhone("8"+ph[2:])); err != nil {
		t.Fatal(err)
	}
	codes := e.tg.Codes()
	if len(codes) != 1 || !verifycode.CodeRe.MatchString(codes[0]) {
		t.Fatalf("codes = %v", codes)
	}
	r := e.tg.Requests()[0]
	if r.Auth != "Bearer "+gatewayToken || r.Body["phone_number"] != ph || r.Body["ttl"] != float64(300) {
		t.Fatalf("request = %+v", r)
	}
	if n := count(t, "SELECT count(*) FROM password_recovery_codes WHERE user_id = $1 AND code_hash <> $2", id, codes[0]); n != 1 {
		t.Fatalf("hashed codes = %d", n)
	}
	if _, err := e.recovery.Reset(ctx, ph, codes[0], "new-secret"); err != nil {
		t.Fatal(err)
	}
	if !passwordIs(t, id, "new-secret") || len(e.signedOut) != 1 || e.signedOut[0] != id {
		t.Fatalf("password not changed or not signed out: %v", e.signedOut)
	}
	if n := count(t, "SELECT count(*) FROM password_recovery_codes WHERE user_id = $1", id); n != 0 {
		t.Fatalf("codes left = %d", n)
	}
	if n := count(t, "SELECT count(*) FROM platform_logs WHERE user_id = $1 AND event IN ('user.password_code_sent', 'user.password_recovered')", id); n != 2 {
		t.Fatalf("platform logs = %d", n)
	}
	if _, err := e.recovery.Reset(ctx, ph, codes[0], "again-secret"); kind(err) != verifycode.KindCodeExpired {
		t.Fatalf("code reused: %v", err)
	}
}

// GS-238: неизвестный номер — тот же ответ, ничего не отправлено.
func TestPasswordRecovery_UnknownNumberSameAnswer(t *testing.T) {
	e := newEnv(t, true)
	if err := e.recovery.SendCode(context.Background(), "+77009998877"); err != nil {
		t.Fatal(err)
	}
	if len(e.tg.Requests()) != 0 {
		t.Fatal("a code was sent")
	}
}

// GS-239: один код в минуту, пять в час, пять попыток на код, код живёт пять минут.
func TestPasswordRecovery_Limits(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	ph := phone()
	id := user(t, ph, false)
	send := func() error { return e.recovery.SendCode(ctx, ph) }
	reset := func(code string) error { _, err := e.recovery.Reset(ctx, ph, code, "new-secret"); return err }

	if err := send(); err != nil {
		t.Fatal(err)
	}
	err := send()
	var ve *verifycode.Error
	if !errors.As(err, &ve) || ve.Kind != verifycode.KindWait || ve.Field != "phone" || ve.Params["seconds"] != "60" {
		t.Fatalf("second send: %v", err)
	}
	e.clock.travel(15 * time.Second)
	if err := send(); !errors.As(err, &ve) || ve.Error() != "Новый код можно запросить через 45 с." {
		t.Fatalf("wait text: %v", err)
	}
	for range 5 {
		if err := reset("000000"); kind(err) != verifycode.KindCodeWrong {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if err := reset(e.tg.Codes()[0]); kind(err) != verifycode.KindCodeExpired {
		t.Fatalf("attempts spent: %v", err)
	}
	e.clock.travel(61 * time.Second)
	if err := send(); err != nil {
		t.Fatal(err)
	}
	e.clock.travel(6 * time.Minute)
	if err := reset(e.tg.Codes()[1]); kind(err) != verifycode.KindCodeExpired {
		t.Fatalf("expired code: %v", err)
	}
	for range 3 {
		e.clock.travel(61 * time.Second)
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.travel(61 * time.Second)
	if err := send(); kind(err) != verifycode.KindHourly {
		t.Fatalf("sixth code in an hour: %v", err)
	}
	if len(e.tg.Codes()) != 5 || !passwordIs(t, id, "old-secret") {
		t.Fatalf("codes = %d", len(e.tg.Codes()))
	}
}

// GS-240: без Gateway — «не подключено»; Telegram не принял номер — ошибка, кода нет; заблокированному — ничего.
func TestPasswordRecovery_NoGatewayOrRejectedOrBlocked(t *testing.T) {
	ctx := context.Background()
	ph := phone()
	id := user(t, ph, false)
	e := newEnv(t, false)
	if ok, _ := e.recovery.Available(ctx); ok {
		t.Fatal("available without gateway")
	}
	if err := e.recovery.SendCode(ctx, ph); kind(err) != verifycode.KindUnavailable {
		t.Fatalf("no gateway: %v", err)
	}
	e = newEnv(t, true)
	e.tg.OnGateway(telegramtest.GatewayFails)
	if err := e.recovery.SendCode(ctx, ph); kind(err) != verifycode.KindNotSent {
		t.Fatalf("rejected: %v", err)
	}
	if n := count(t, "SELECT count(*) FROM password_recovery_codes WHERE user_id = $1", id); n != 0 {
		t.Fatalf("codes = %d", n)
	}
	// Сразу можно снова: неотправленный код лимит не тратит.
	e.tg.OnGateway(nil)
	if err := e.recovery.SendCode(ctx, ph); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	blocked := phone()
	user(t, blocked, true)
	before := len(e.tg.Requests())
	if err := e.recovery.SendCode(ctx, blocked); err != nil || len(e.tg.Requests()) != before {
		t.Fatalf("blocked: %v", err)
	}
}

// transferPair — клуб с переводами, Арман (100 ₸) и Дана (10 ₸), принятая дружба.
func transferPair(t *testing.T) (club, arman, dana, friendship int64) {
	t.Helper()
	club = tdb.Club(t, fmt.Sprintf("tc%d", seq.Add(1)))
	arman, dana = user(t, phone(), false), user(t, phone(), false)
	tdb.Join(t, club, arman)
	tdb.Join(t, club, dana)
	for u, amount := range map[int64]money.Tiyn{arman: 10000, dana: 1000} {
		if err := db.InTenantTx(context.Background(), tdb.App, club, func(q *store.Queries) error {
			_, err := billing.SetInitialBalance(context.Background(), q, billing.InitialBalanceParams{ClubID: club, UserID: u, Main: amount})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return club, arman, dana, tdb.Friendship(t, club, arman, dana, true)
}

// confirm — FriendTransferCodes::confirm(): неверный код засчитывает попытку отдельной транзакцией.
func confirm(t *testing.T, e *env, club, userID, friendship int64, amount, code string) (billing.FriendTransferResult, error) {
	t.Helper()
	ctx := context.Background()
	var res billing.FriendTransferResult
	err := db.InTenantTxWith(ctx, tdb.App, club, db.Options{UserID: userID}, func(q *store.Queries) error {
		var err error
		res, err = billing.ConfirmFriendTransferCode(ctx, q, billing.ConfirmTransferCodeParams{
			ClubID: club, UserID: userID, FriendshipID: friendship, AmountInput: amount, Code: code, Now: e.clock.Now(),
			VerifyCode: func(hash, code string) bool { _, err := pw.Verify(ctx, code, hash); return err == nil },
		})
		return err
	})
	var mm *billing.TransferCodeMismatchError
	if errors.As(err, &mm) {
		if e2 := db.InTenantTx(ctx, tdb.App, club, func(q *store.Queries) error {
			return billing.RecordTransferCodeAttempt(ctx, q, club, mm.CodeID)
		}); e2 != nil {
			t.Fatal(e2)
		}
	}
	return res, err
}

// GS-521: код не шлётся для невозможного перевода; привязан к другу и сумме; повтор раньше 60 с — ошибка code;
// верный код и сумма — перевод, коды удалены; код одноразовый.
func TestFriendTransferCode_TelegramConfirmOnce(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	club, arman, dana, f := transferPair(t)

	var be *billing.BalanceError
	if err := e.transfer.Send(ctx, club, arman, f, 50000); !errors.As(err, &be) || be.Field != "amount" {
		t.Fatalf("500 > balance: %v", err)
	}
	if len(e.tg.Requests()) != 0 {
		t.Fatal("code sent for an impossible transfer")
	}
	if err := e.transfer.Send(ctx, club, arman, f, 4000); err != nil {
		t.Fatal(err)
	}
	code := e.tg.Codes()[0]
	var ve *verifycode.Error
	if err := e.transfer.Send(ctx, club, arman, f, 4000); !errors.As(err, &ve) || ve.Field != "code" || ve.Kind != verifycode.KindWait {
		t.Fatalf("resend: %v", err)
	}
	wrong := "111111"
	if code == wrong {
		wrong = "222222"
	}
	if _, err := confirm(t, e, club, arman, f, "40", wrong); err == nil {
		t.Fatal("wrong code accepted")
	}
	if _, err := confirm(t, e, club, arman, f, "90", code); err == nil {
		t.Fatal("other amount accepted")
	}
	if n := count(t, "SELECT attempts FROM transfer_codes WHERE user_id = $1", arman); n != 2 {
		t.Fatalf("attempts = %d", n)
	}
	res, err := confirm(t, e, club, arman, f, "40", code)
	if err != nil || res.Balance != 6000 {
		t.Fatalf("confirm: %+v, %v", res, err)
	}
	if m, _ := tdb.Balances(t, club, dana); m != 5000 {
		t.Fatalf("dana = %d", m)
	}
	if n := count(t, "SELECT count(*) FROM transfer_codes WHERE user_id = $1", arman); n != 0 {
		t.Fatalf("codes left = %d", n)
	}
	if _, err := confirm(t, e, club, arman, f, "40", code); err == nil {
		t.Fatal("code reused")
	}
	if m, _ := tdb.Balances(t, club, arman); m != 6000 {
		t.Fatalf("arman = %d", m)
	}
}

// GS-522 (часть сервиса): пять неверных попыток — верный код уже не работает. Лимиты — на аккаунт во всех клубах.
func TestFriendTransferCode_FiveAttemptsAndLimitsAcrossClubs(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	club, arman, _, f := transferPair(t)
	if err := e.transfer.Send(ctx, club, arman, f, 1000); err != nil {
		t.Fatal(err)
	}
	code := e.tg.Codes()[0]
	wrong := "111111"
	if code == wrong {
		wrong = "222222"
	}
	for range 5 {
		if _, err := confirm(t, e, club, arman, f, "10", wrong); err == nil {
			t.Fatal("wrong code accepted")
		}
	}
	if _, err := confirm(t, e, club, arman, f, "10", code); err == nil {
		t.Fatal("code works after five wrong attempts")
	}
	if m, _ := tdb.Balances(t, club, arman); m != 10000 {
		t.Fatalf("arman = %d", m)
	}

	// Второй клуб того же аккаунта: повтор раньше 60 с запрещён и там.
	club2 := tdb.Club(t, fmt.Sprintf("tc%d", seq.Add(1)))
	friend2 := user(t, phone(), false)
	tdb.Join(t, club2, arman)
	tdb.Join(t, club2, friend2)
	if err := db.InTenantTx(ctx, tdb.App, club2, func(q *store.Queries) error {
		_, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{ClubID: club2, UserID: arman, Main: 10000})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f2 := tdb.Friendship(t, club2, arman, friend2, true)
	if err := e.transfer.Send(ctx, club2, arman, f2, 1000); kind(err) != verifycode.KindWait {
		t.Fatalf("other club within 60 s: %v", err)
	}
	// Чужая дружба (другого клуба) — 404.
	if err := e.transfer.Send(ctx, club2, arman, f, 1000); !errors.Is(err, billing.ErrFriendshipNotFound) {
		t.Fatalf("friendship of another club: %v", err)
	}
	e.clock.travel(61 * time.Second)
	if err := e.transfer.Send(ctx, club2, arman, f2, 1000); err != nil {
		t.Fatal(err)
	}
	if n := count(t, "SELECT count(*) FROM transfer_codes WHERE user_id = $1 AND club_id = $2", arman, club2); n != 1 {
		t.Fatalf("club2 codes = %d", n)
	}
}

// Без Gateway код перевода не отправить.
func TestFriendTransferCode_NotConnected(t *testing.T) {
	e := newEnv(t, false)
	club, arman, _, f := transferPair(t)
	if err := e.transfer.Send(context.Background(), club, arman, f, 1000); kind(err) != verifycode.KindUnavailable {
		t.Fatalf("not connected: %v", err)
	}
}
