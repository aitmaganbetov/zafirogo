package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Сгорание просроченных пакетов времени (time_packages).
//
// Решение Q-011, GAP-014 (docs/DECISIONS.md). В старом коде просроченный пакет при попытке запуска не удалялся:
// $package->delete() откатывался вместе с исключением «Срок абонемента истёк.» (AgentTariffPurchaseService::startPackage(),
// стр. 171–203), а суточная задача session-purchases:prune (routes/console.php) удаляла просроченные пакеты молча:
// TimePackage::where('expires_at', '<', now())->delete(). Здесь пакет сгорает так же, но с записью: ledger минут
// (операция package_expired, минус остаток пакета) и журнал действий time.package_expired — в одной транзакции
// с удалением пакета.

// PackageExpiredEvent — код события журнала о сгорании пакета.
const PackageExpiredEvent = "time.package_expired"

// burnPackage удаляет пакет pkg (уже заблокированный FOR UPDATE в транзакции q клуба) с записью сгорания.
// actorUserID — кто вызвал сгорание (0 — система); meta — данные запроса для журнала (nil — из контекста).
func burnPackage(ctx context.Context, q *store.Queries, clubID int64, pkg store.LockTimePackageRow, now time.Time,
	actorUserID int64, meta *activity.RequestMeta) error {
	if pkg.MinutesLeft > 0 {
		var tariffID int64
		if pkg.TariffID != nil {
			tariffID = *pkg.TariffID
		}
		body, err := json.Marshal(map[string]any{
			"package_id": pkg.ID, "name": pkg.Name, "expires_at": ISO8601(pkg.ExpiresAt), "lost_minutes": pkg.MinutesLeft,
		})
		if err != nil {
			return err
		}
		if _, err := ledger.Post(ctx, q, ledger.Operation{
			ClubID: clubID, Kind: ledger.KindPackageExpired,
			IdempotencyKey: ledgerKey("package_expired", strconv.FormatInt(pkg.ID, 10)),
			Minutes:        []ledger.MinuteEntry{{UserID: pkg.UserID, TimePackageID: pkg.ID, TariffID: tariffID, Minutes: -pkg.MinutesLeft}},
			Meta:           body, ActorUserID: actorUserID,
		}); err != nil {
			return fmt.Errorf("service: package %d expired ledger: %w", pkg.ID, err)
		}
	}
	if err := q.DeleteTimePackage(ctx, store.DeleteTimePackageParams{ClubID: clubID, ID: pkg.ID}); err != nil {
		return err
	}
	e := activity.Entry{
		ClubID: clubID, Event: PackageExpiredEvent,
		// Ключ перевода: «Абонемент :name сгорел: :minutes мин.» (новый текст; Решение Q-011).
		Description: "Абонемент " + pkg.Name + " сгорел: " + itoa(pkg.MinutesLeft) + " мин.",
		UserID:      pkg.UserID, SubjectType: SubjectUser, SubjectID: pkg.UserID,
		Details: map[string]any{
			"package_id": pkg.ID, "name": pkg.Name, "minutes": pkg.MinutesLeft, "expires_at": ISO8601(pkg.ExpiresAt),
		},
		Meta: meta, At: now,
	}
	if meta != nil {
		_, err := activity.Record(ctx, q, e)
		return err
	}
	return RecordActivity(ctx, q, e)
}

// burnIfExpired — после отката запуска с ErrPackageExpired: отдельная транзакция, которая снова блокирует пакет
// и сжигает его, если он всё ещё не запускается (параллельный запрос мог его уже удалить).
// Решение Q-011 (docs/DECISIONS.md).
func (s *Service) burnIfExpired(ctx context.Context, req PackageStartRequest) error {
	return s.tx(ctx, req.ClubID, func(q *store.Queries) error {
		now := s.clock.Now()
		pkg, err := q.LockTimePackage(ctx, store.LockTimePackageParams{ClubID: req.ClubID, UserID: req.UserID, ID: req.PackageID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := pricing.PackageStartMinutes(pkg.MinutesLeft, pkg.ExpiresAt, now); !errors.Is(err, pricing.ErrPackageExpired) {
			return err
		}
		return burnPackage(ctx, q, req.ClubID, pkg, now, req.Actor.UserID, nil)
	})
}

// ExpiredPackage — сгоревший пакет.
type ExpiredPackage struct {
	ClubID, PackageID, UserID, Minutes int64
}

// BurnExpiredPackages сжигает все пакеты, у которых срок вышел (expires_at < now), во всех клубах — как суточная
// задача session-purchases:prune старого кода (routes/console.php), но с записью в ledger минут и журнал
// (Решение Q-011 (docs/DECISIONS.md)). platform — пул zafiro_platform (список по всем клубам), app — пул
// zafiro_app: каждый пакет сгорает в своей транзакции клуба (ошибка одного не мешает остальным).
// meta — данные «запроса» для журнала (фоновая задача: IP 127.0.0.1).
func BurnExpiredPackages(ctx context.Context, platform, app db.Beginner, now time.Time, meta activity.RequestMeta) ([]ExpiredPackage, error) {
	var due []store.ListExpiredTimePackagesRow
	if err := db.InPlatformTx(ctx, platform, func(q *store.Queries) error {
		var err error
		due, err = q.ListExpiredTimePackages(ctx, now)
		return err
	}); err != nil {
		return nil, err
	}
	var (
		out  []ExpiredPackage
		errs []error
	)
	for _, d := range due {
		var burned *ExpiredPackage
		err := db.InTenantTx(ctx, app, d.ClubID, func(q *store.Queries) error {
			burned = nil
			pkg, err := q.LockTimePackage(ctx, store.LockTimePackageParams{ClubID: d.ClubID, UserID: d.UserID, ID: d.ID})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // уже запущен или сожжён
			}
			if err != nil {
				return err
			}
			if !pkg.ExpiresAt.Before(now) {
				return nil
			}
			if err := burnPackage(ctx, q, d.ClubID, pkg, now, 0, &meta); err != nil {
				return err
			}
			burned = &ExpiredPackage{ClubID: d.ClubID, PackageID: pkg.ID, UserID: pkg.UserID, Minutes: pkg.MinutesLeft}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("club %d package %d: %w", d.ClubID, d.ID, err))
			continue
		}
		if burned != nil {
			out = append(out, *burned)
		}
	}
	return out, errors.Join(errs...)
}
