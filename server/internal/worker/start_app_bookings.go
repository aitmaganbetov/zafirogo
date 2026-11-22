package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// StartAppBookingsJob — имя задачи и ключ advisory lock (hashtext('job:<name>')).
const StartAppBookingsJob = "app-bookings:start"

// StartAppBookingsInterval — раз в минуту: оплаченное время брони начинается не позже чем через минуту после того,
// как подошёл интервал до её начала (в старом — на ближайшем heartbeat ПК, раз в несколько секунд).
const StartAppBookingsInterval = time.Minute

// startAppBookingsMaxGap — наибольший интервал между бронями клуба, минут (правило настроек: до 10 080); бронь из
// приложения — не дальше 7 дней вперёд. Оценка интервала в запросе этим ограничена.
const startAppBookingsMaxGap = 7 * 24 * 60

// startAppBookingsBatch — строк на страницу. За тик проходятся все страницы (по (ends_at, id)): кандидатов —
// оплаченных ожидающих броней, которые ещё не закончились и до начала которых осталось не больше интервала клуба, —
// немного, но хвост не откладывается на следующий тик.
const startAppBookingsBatch = 1000

// StartAppBookings — запуск оплаченного времени брони из приложения для гостей, которые УЖЕ сидят за своим
// забронированным ПК (решение UQ-11, docs/open-questions/upstream-bookings.md).
//
// В старом Laravel это проверялось на каждом heartbeat ПК с токеном клиента (ComputerReservations::
// startForSignedIn(): запрос в базу на каждый пинг). Здесь heartbeat в базу не ходит. Время начинается двумя путями:
//
//  1. событие: гость входит на ПК (вход, подтверждение QR) — обработчик входа агента сам зовёт
//     reservations.StartForSignedIn (тема Ж);
//  2. эта задача: раз в минуту берёт из базы оплаченные ожидающие брони, до начала которых осталось не больше
//     интервала клуба (одним запросом по частичному индексу, по всем клубам), и для каждой спрашивает присутствие
//     (Valkey), на каком ПК сейчас вошёл её гость. Вошёл на забронированном — StartForSignedIn в транзакции клуба
//     (условия те же, что в старом, проверяются под блокировкой ПК и брони; запуск идемпотентен по arrived_at).
//
// Один исполнитель на кластер — advisory lock на тик (leader.RunOnce, Q-086); двойной запуск при коротком
// раздвоении лидерства безвреден: вторая транзакция увидит arrived_at.
type StartAppBookings struct {
	// Platform — пул zafiro_platform (брони по всем клубам и advisory lock).
	Platform *pgxpool.Pool
	// App — пул zafiro_app (запуск в транзакции клуба, RLS).
	App db.Beginner
	// SignedIn — ПК клуба, на которых сейчас вошёл гость (присутствие агентов). nil — задача ничего не делает.
	SignedIn func(ctx context.Context, clubID, userID int64) (computerIDs []int64, err error)
	// Notifier — StartSession агенту ПК после коммита (nil — агент узнает при следующем подключении).
	Notifier billingsvc.SessionNotifier
	// Now — часы (nil — time.Now).
	Now func() time.Time
	// Interval — период (0 — StartAppBookingsInterval).
	Interval time.Duration
	// Batch — строк на страницу (0 — startAppBookingsBatch); тесты.
	Batch int32
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// Run выполняет тики до отмены ctx. Первый тик — сразу.
func (w *StartAppBookings) Run(ctx context.Context) error {
	return loop{
		job: StartAppBookingsJob, failed: "start app bookings tick failed", log: w.Log,
		interval: intervalOr(w.Interval, StartAppBookingsInterval), fixedRate: true,
	}.run(ctx, func(ctx context.Context) error { _, _, err := w.Tick(ctx); return err })
}

// Tick — один проход под локом: ran = false — тик выполняет другой инстанс; n — сколько броней запущено.
func (w *StartAppBookings) Tick(ctx context.Context) (ran bool, n int, err error) {
	if w.SignedIn == nil {
		return false, 0, nil
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	ran, err = leader.RunOnce(ctx, w.Platform, StartAppBookingsJob, func(ctx context.Context, _ *pgxpool.Conn) error {
		batch := w.Batch
		if batch <= 0 {
			batch = startAppBookingsBatch
		}
		var errs []error
		// Курсор: (ends_at, id) последней строки страницы. Запущенная бронь из выборки выпадает (arrived_at), но
		// курсор уже за ней — страницы не сдвигаются.
		afterEnds, afterID := now, int64(0)
		for {
			var due []store.AppBookingDueCandidatesRow
			if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
				var err error
				due, err = q.AppBookingDueCandidates(ctx, store.AppBookingDueCandidatesParams{
					Now: now, AfterEndsAt: afterEnds, AfterID: afterID,
					DefaultGap: int32(billingsvc.DefaultBookingGapMinutes), MaxGap: startAppBookingsMaxGap, MaxRows: batch,
				})
				return err
			}); err != nil {
				return errors.Join(append(errs, err)...)
			}
			for _, d := range due {
				// Точный интервал клуба (в запросе — оценка сверху): бронь можно начинать с starts_at − интервал.
				gap := time.Duration(billingsvc.BookingGapMinutes(d.GapSettings)) * time.Minute
				if d.UserID == nil || d.StartsAt.After(now.Add(gap)) {
					continue
				}
				pcs, err := w.SignedIn(ctx, d.ClubID, *d.UserID)
				if err != nil {
					errs = append(errs, fmt.Errorf("club %d user %d presence: %w", d.ClubID, *d.UserID, err))
					continue
				}
				if !slices.Contains(pcs, d.ClubComputerID) {
					continue
				}
				started, err := w.start(ctx, d.ClubID, d.ClubComputerID, *d.UserID, now)
				if err != nil {
					errs = append(errs, fmt.Errorf("club %d reservation %d: %w", d.ClubID, d.ID, err))
					continue
				}
				if started {
					n++
				}
			}
			if len(due) < int(batch) {
				break
			}
			afterEnds, afterID = due[len(due)-1].EndsAt, due[len(due)-1].ID
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
		}
		if n > 0 {
			logOr(w.Log).Info("paid app bookings started", "job", StartAppBookingsJob, "count", n)
		}
		return errors.Join(errs...)
	})
	return ran, n, err
}

// start — StartForSignedIn в транзакции клуба; уведомление агента — после коммита.
func (w *StartAppBookings) start(ctx context.Context, clubID, computerID, userID int64, now time.Time) (bool, error) {
	ctx, hooks := billingsvc.WithCommitHooks(ctx)
	var started bool
	err := db.InTenantTx(ctx, w.App, clubID, func(q *store.Queries) error {
		hooks.Reset()
		started = false
		info, err := club.Load(ctx, q, clubID)
		if err != nil {
			return err
		}
		// Журнал reservation.arrived — от имени гостя, как в старом; системное действие: IP 127.0.0.1.
		_, started, err = reservations.StartForSignedIn(ctx, q, info, computerID, userID, reservations.Env{
			Now: now, Actor: billing.BalanceActor{UserID: userID, Kind: "system"}, Notifier: w.Notifier,
			Meta: &activity.RequestMeta{IP: "127.0.0.1"},
		})
		return err
	})
	if err != nil {
		return false, err
	}
	hooks.Run(context.WithoutCancel(ctx))
	return started, nil
}
