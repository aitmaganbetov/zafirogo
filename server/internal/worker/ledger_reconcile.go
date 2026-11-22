package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// LedgerReconcileJob — имя задачи и ключ advisory lock.
const LedgerReconcileJob = "ledger:reconcile"

// Периоды сверки по умолчанию (workers.ledger_reconcile.interval / full_interval).
const (
	LedgerReconcileInterval     = time.Hour
	LedgerReconcileFullInterval = 24 * time.Hour
)

const (
	// ledgerReconcileRetry — повтор после ошибки тика.
	ledgerReconcileRetry = 5 * time.Minute
	// ledgerReconcileOverlap — запас частой сверки назад от прошлой успешной: транзакция, начавшаяся до прошлой
	// сверки и закоммитившаяся после её снимка, попадёт в следующую (created_at/updated_at — начало транзакции).
	ledgerReconcileOverlap = 10 * time.Minute
	// ledgerReconcileClubsPage — клубов за одно чтение списка.
	ledgerReconcileClubsPage = 200
	// LedgerReconcileMaxFindings — не больше стольких расхождений одного вида на клуб записывается за проход:
	// испорченная целиком проекция (неудачное восстановление) не должна залить журнал платформы десятками тысяч
	// строк. Остальные будут записаны, когда первые исчезнут; полное число — в журнале сервера.
	LedgerReconcileMaxFindings = 200
)

// События журнала платформы и виды расхождений (ledger_reconcile_findings.kind, 0033).
const (
	EventLedgerDriftFound    = "ledger.drift_found"
	EventLedgerDriftResolved = "ledger.drift_resolved"

	findingMemberBalance = "member_balance"
	findingUnbalanced    = "unbalanced_transaction"
	findingCashSnapshot  = "cash_drawer_snapshot"
)

// Тексты журнала платформы (ключи перевода; суммы — целые тиыны со знаком: «проекция − журнал»).
const (
	msgDriftMember     = "Сверка с журналом операций: клуб :club, клиент №:id — баланс расходится на :main тиын, бонусы — на :bonus тиын."
	msgDriftUnbalanced = "Сверка с журналом операций: клуб :club, операция №:id — сумма проводок :sum тиын вместо нуля."
	msgDriftCash       = "Сверка с журналом операций: клуб :club, смена №:id — снимок остатка кассы расходится с журналом на :diff тиын."
	msgDriftResolved   = "Сверка с журналом операций: клуб :club — расхождение №:finding больше не наблюдается."
)

// Морф-классы предметов записей журнала платформы (как в кабинете суперадмина, PD-14).
const (
	subjectUser      = `App\Models\User`
	subjectCashShift = `App\Models\CashShift`
	subjectLedgerTx  = "ledger_transaction"
)

// LedgerReconcile — фоновая сверка проекций денег с журналом операций (аудит AC-3). Новая функция: в Laravel не
// было ни журнала, ни сверки.
//
// Что сверяется в каждом клубе:
//   - member_balances (balance, bonus_balance) = Σ проводок по счетам клиента;
//   - Σ проводок каждой операции = 0 (двойная запись);
//   - снимок остатка кассы последней закрытой смены = остатку по журналу на момент снимка (только полный проход).
//
// Сверка НИЧЕГО не исправляет. Расхождение записывается в ledger_reconcile_findings и один раз — в журнал
// платформы (ledger.drift_found); повторные проходы молчат, пока разница не изменилась; исчезло — запись
// ledger.drift_resolved. Число открытых расхождений и время последней успешной сверки — метрики Prometheus
// (observability: ledger_reconcile_*), по ним оповещения ZafiroLedgerDrift и ZafiroLedgerReconcileStale.
//
// Нагрузка. Частый проход (Interval, час) читает только клиентов, чей баланс менялся с прошлой сверки, и операции
// за это время; полный (FullInterval, сутки) — все проводки клуба. Клубы идут по одному, каждый запрос — своя
// короткая транзакция клуба в пуле zafiro_app (RLS; предел команды db.StatementTimeout), общей транзакции нет.
// Замеры на клубе с 50 000 клиентов и 1 млн проводок — docs/open-questions/audit-code.md AC-3.
//
// Один исполнитель на кластер — leader.RunOnce (Q-086); «пора ли» решает общая строка ledger_reconcile_state,
// поэтому тики нескольких инстансов не повторяют сверку чаще Interval.
type LedgerReconcile struct {
	// Platform — пул zafiro_platform: advisory lock, список клубов, состояние, находки, журнал платформы.
	Platform *pgxpool.Pool
	// App — пул zafiro_app: запросы сверки в транзакции клуба (RLS).
	App db.Beginner
	// Interval — период частой сверки (0 — LedgerReconcileInterval).
	Interval time.Duration
	// FullInterval — период полной сверки (0 — LedgerReconcileFullInterval).
	FullInterval time.Duration
	// MaxFindings — предел записываемых расхождений одного вида на клуб за проход (0 — LedgerReconcileMaxFindings).
	MaxFindings int
	Now         func() time.Time
	Log         *slog.Logger
}

// LedgerReconcileResult — итог тика.
type LedgerReconcileResult struct {
	// Ran — лок взят (false — сверку сейчас выполняет другой инстанс).
	Ran bool
	// Skipped — сверка недавно прошла (на этом или другом инстансе): проход не выполнялся.
	Skipped bool
	// Full — полный проход.
	Full  bool
	Clubs int
	// Found — новые и изменившиеся расхождения (записи ledger.drift_found), Resolved — исчезнувшие, Open — открытых
	// после прохода (в проверенных клубах).
	Found, Resolved, Open int
}

// Run выполняет тики до отмены ctx (первый — сразу: если сверка недавно прошла, тик ничего не делает).
func (w *LedgerReconcile) Run(ctx context.Context) error {
	return loop{
		job: LedgerReconcileJob, failed: "ledger reconcile tick failed", log: w.Log,
		interval: w.tickInterval(), retry: ledgerReconcileRetry,
	}.run(ctx, func(ctx context.Context) error { _, err := w.Tick(ctx); return err })
}

func (w *LedgerReconcile) interval() time.Duration {
	return intervalOr(w.Interval, LedgerReconcileInterval)
}

// tickInterval — как часто инстанс проверяет «пора ли»: чаще периода сверки, чтобы после падения лидера или сбоя
// прохода сверку подхватили без ожидания целого периода.
func (w *LedgerReconcile) tickInterval() time.Duration {
	return max(w.interval()/4, time.Second)
}

func (w *LedgerReconcile) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *LedgerReconcile) maxFindings() int {
	if w.MaxFindings > 0 {
		return w.MaxFindings
	}
	return LedgerReconcileMaxFindings
}

// Tick — один запуск под локом: сверка, если с прошлой успешной прошёл Interval.
func (w *LedgerReconcile) Tick(ctx context.Context) (LedgerReconcileResult, error) {
	var res LedgerReconcileResult
	ran, err := leader.RunOnce(ctx, w.Platform, LedgerReconcileJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		return w.pass(ctx, store.New(conn), &res)
	})
	res.Ran = ran
	return res, err
}

func (w *LedgerReconcile) pass(ctx context.Context, state *store.Queries, res *LedgerReconcileResult) error {
	started := w.now()
	st, err := state.ReconcileState(ctx)
	if err != nil {
		return fmt.Errorf("ledger reconcile: state: %w", err)
	}
	if st.LastSuccessAt != nil && started.Sub(*st.LastSuccessAt) < w.interval() {
		res.Skipped = true
		return nil
	}
	// Полный проход: первый запуск, вышел срок или частому не от чего считать.
	res.Full = st.LastSuccessAt == nil || st.LastFullAt == nil ||
		started.Sub(*st.LastFullAt) >= intervalOr(w.FullInterval, LedgerReconcileFullInterval)
	var since time.Time
	if !res.Full {
		since = st.LastSuccessAt.Add(-ledgerReconcileOverlap)
	}
	if err := state.ReconcileStarted(ctx, started); err != nil {
		return fmt.Errorf("ledger reconcile: state: %w", err)
	}

	var failed []error
	var after int64
	for {
		clubs, err := state.ReconcileClubs(ctx, store.ReconcileClubsParams{AfterID: after, Lim: ledgerReconcileClubsPage})
		if err != nil {
			failed = append(failed, fmt.Errorf("clubs: %w", err))
			break
		}
		for _, c := range clubs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Ошибка одного клуба не останавливает сверку остальных, но проход успешным не считается.
			if err := w.club(ctx, c, res.Full, since, started, res); err != nil {
				logOr(w.Log).Error("ledger reconcile: club failed", "job", LedgerReconcileJob, "club_id", c.ID, "err", err)
				failed = append(failed, fmt.Errorf("club %d: %w", c.ID, err))
				continue
			}
			res.Clubs++
		}
		if len(clubs) < ledgerReconcileClubsPage {
			break
		}
		after = clubs[len(clubs)-1].ID
	}

	elapsed := time.Since(started)
	if w.Now != nil {
		elapsed = w.now().Sub(started)
	}
	if len(failed) > 0 {
		err := errors.Join(failed...)
		msg := err.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		if serr := state.ReconcileFailed(ctx, store.ReconcileFailedParams{DurationMs: elapsed.Milliseconds(), Error: msg}); serr != nil {
			logOr(w.Log).Error("ledger reconcile: state", "job", LedgerReconcileJob, "err", serr)
		}
		return fmt.Errorf("ledger reconcile: %w", err)
	}
	if err := state.ReconcileSucceeded(ctx, store.ReconcileSucceededParams{
		Now: started, FullPass: res.Full, DurationMs: elapsed.Milliseconds(), Clubs: int32(min(res.Clubs, 1<<30)), // #nosec G115 -- ограничено min
	}); err != nil {
		return fmt.Errorf("ledger reconcile: state: %w", err)
	}
	attrs := []any{"job", LedgerReconcileJob, "full", res.Full, "clubs", res.Clubs, "found", res.Found,
		"resolved", res.Resolved, "open", res.Open, "duration_ms", elapsed.Milliseconds()}
	if res.Open > 0 || res.Found > 0 {
		logOr(w.Log).Error("ledger reconcile: balances do not match the ledger", attrs...)
	} else {
		logOr(w.Log).Info("ledger reconcile: ok", attrs...)
	}
	return nil
}

// drift — расхождение, найденное сейчас.
type drift struct {
	kind        string
	subjectID   int64
	fingerprint string
	details     map[string]any
	// Запись журнала платформы.
	key         string
	params      map[string]string
	subjectType string
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// club сверяет один клуб и записывает изменения в находках.
func (w *LedgerReconcile) club(ctx context.Context, c store.ReconcileClubsRow, full bool, since, now time.Time, res *LedgerReconcileResult) error {
	var open []store.ReconcileOpenFindingsRow
	if err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		var err error
		open, err = q.ReconcileOpenFindings(ctx, c.ID)
		return err
	}); err != nil {
		return fmt.Errorf("open findings: %w", err)
	}
	var openMembers []int64
	for _, f := range open {
		if f.Kind == findingMemberBalance {
			openMembers = append(openMembers, f.SubjectID)
		}
	}

	limit := w.maxFindings()
	var found []drift
	// checked — виды, по которым проход видел все возможные расхождения: открытую находку такого вида, которой
	// сейчас нет, можно закрыть. Частый проход старые операции и снимок кассы не перечитывает.
	checked := map[string]bool{findingMemberBalance: true}

	// 1. Балансы клиентов. Каждый запрос — своя короткая транзакция клуба (одна команда = один снимок данных).
	var members []store.ListMemberBalanceDriftRow
	if err := db.InTenantTx(ctx, w.App, c.ID, func(q *store.Queries) error {
		members = nil
		if full {
			var err error
			members, err = q.ListMemberBalanceDrift(ctx, c.ID)
			return err
		}
		rows, err := q.ReconcileMemberBalancesTouched(ctx, store.ReconcileMemberBalancesTouchedParams{
			ClubID: c.ID, Since: since, UserIds: openMembers,
		})
		for _, r := range rows {
			members = append(members, store.ListMemberBalanceDriftRow(r))
		}
		return err
	}); err != nil {
		return fmt.Errorf("member balances: %w", err)
	}
	if len(members) > limit {
		logOr(w.Log).Error("ledger reconcile: more member balance drifts than recorded", "job", LedgerReconcileJob,
			"club_id", c.ID, "drifts", len(members), "recorded", limit)
	}
	for _, m := range members {
		main, bonus := m.Balance-m.LedgerMain, m.BonusBalance-m.LedgerBonus
		found = append(found, drift{
			kind: findingMemberBalance, subjectID: m.UserID, subjectType: subjectUser,
			// Отпечаток — сами разницы: клиент продолжает платить (баланс и журнал двигаются вместе) — запись не
			// повторяется; изменилась разница — пишется снова.
			fingerprint: "main=" + itoa(main) + ";bonus=" + itoa(bonus),
			details: map[string]any{
				"balance_tiyn": m.Balance, "ledger_tiyn": m.LedgerMain, "diff_tiyn": main,
				"bonus_balance_tiyn": m.BonusBalance, "ledger_bonus_tiyn": m.LedgerBonus, "bonus_diff_tiyn": bonus,
			},
			key:    msgDriftMember,
			params: map[string]string{"club": c.Name, "id": itoa(m.UserID), "main": itoa(main), "bonus": itoa(bonus)},
		})
	}

	// 2. Двойная запись: операции с суммой проводок не ноль.
	var unbalanced []store.ReconcileUnbalancedTransactionsRow
	if err := db.InTenantTx(ctx, w.App, c.ID, func(q *store.Queries) error {
		var err error
		unbalanced, err = q.ReconcileUnbalancedTransactions(ctx, store.ReconcileUnbalancedTransactionsParams{
			ClubID: c.ID, Since: since, Lim: int32(min(limit+1, 1<<30)), // #nosec G115 -- ограничено min
		})
		return err
	}); err != nil {
		return fmt.Errorf("unbalanced transactions: %w", err)
	}
	// Закрывать находки этого вида можно, только если прочитана вся история и список не обрезан пределом.
	checked[findingUnbalanced] = full && len(unbalanced) <= limit
	for _, t := range unbalanced {
		found = append(found, drift{
			kind: findingUnbalanced, subjectID: t.ID, subjectType: subjectLedgerTx,
			fingerprint: "sum=" + itoa(t.EntriesSum),
			details:     map[string]any{"entries_sum_tiyn": t.EntriesSum, "transaction_created_at": t.CreatedAt.UTC().Format(time.RFC3339)},
			key:         msgDriftUnbalanced,
			params:      map[string]string{"club": c.Name, "id": itoa(t.ID), "sum": itoa(t.EntriesSum)},
		})
	}

	// 3. Снимок остатка кассы (только полный проход: читает все проводки кассы).
	if full {
		checked[findingCashSnapshot] = true
		var snapshots []store.ReconcileCashDrawerSnapshotRow
		if err := db.InTenantTx(ctx, w.App, c.ID, func(q *store.Queries) error {
			var err error
			snapshots, err = q.ReconcileCashDrawerSnapshot(ctx, c.ID)
			return err
		}); err != nil {
			return fmt.Errorf("cash drawer snapshot: %w", err)
		}
		for _, s := range snapshots {
			if diff := s.SnapshotBalance - s.LedgerBalance; diff != 0 {
				found = append(found, drift{
					kind: findingCashSnapshot, subjectID: s.ShiftID, subjectType: subjectCashShift,
					fingerprint: "diff=" + itoa(diff),
					details:     map[string]any{"snapshot_tiyn": s.SnapshotBalance, "ledger_tiyn": s.LedgerBalance, "diff_tiyn": diff},
					key:         msgDriftCash,
					params:      map[string]string{"club": c.Name, "id": itoa(s.ShiftID), "diff": itoa(diff)},
				})
			}
		}
	}

	if len(found) == 0 && len(open) == 0 {
		return nil // обычный случай: всё сходится, писать нечего
	}
	return w.record(ctx, c, open, found, checked, limit, now, res)
}

// record сравнивает найденное с открытыми находками клуба и записывает разницу одной короткой транзакцией:
// новое и изменившееся — находка и запись журнала платформы; прежнее — только отметка времени; исчезнувшее —
// закрытие и запись «больше не наблюдается».
func (w *LedgerReconcile) record(ctx context.Context, c store.ReconcileClubsRow, open []store.ReconcileOpenFindingsRow,
	found []drift, checked map[string]bool, limit int, now time.Time, res *LedgerReconcileResult) error {
	type key struct {
		kind    string
		subject int64
	}
	byKey := make(map[key]store.ReconcileOpenFindingsRow, len(open))
	for _, f := range open {
		byKey[key{f.Kind, f.SubjectID}] = f
	}
	var foundN, resolvedN, openN int
	err := db.InPlatformTx(ctx, w.Platform, func(q *store.Queries) error {
		foundN, resolvedN, openN = 0, 0, 0
		present := make(map[key]bool, len(found))
		recorded := map[string]int{}
		var seen []int64
		for _, d := range found {
			k := key{d.kind, d.subjectID}
			present[k] = true
			details, err := json.Marshal(d.details)
			if err != nil {
				return err
			}
			prev, was := byKey[k]
			switch {
			case was && prev.Fingerprint == d.fingerprint:
				seen = append(seen, prev.ID)
				openN++
				continue
			case was:
				if err := q.ReconcileChangeFinding(ctx, store.ReconcileChangeFindingParams{
					ClubID: c.ID, ID: prev.ID, Fingerprint: d.fingerprint, Details: details, Now: now,
				}); err != nil {
					return err
				}
			default:
				if recorded[d.kind] >= limit {
					continue // сверх предела — в следующие проходы
				}
				recorded[d.kind]++
				if _, err := q.ReconcileInsertFinding(ctx, store.ReconcileInsertFindingParams{
					ClubID: c.ID, Kind: d.kind, SubjectID: d.subjectID, Fingerprint: d.fingerprint, Details: details, Now: now,
				}); err != nil {
					return err
				}
			}
			openN++
			foundN++
			if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{
				Event: EventLedgerDriftFound, Key: d.key, Params: d.params, ClubID: c.ID,
				SubjectType: d.subjectType, SubjectID: d.subjectID, Details: d.details, At: now,
			}); err != nil {
				return err
			}
		}
		if len(seen) > 0 {
			if err := q.ReconcileSeenFindings(ctx, store.ReconcileSeenFindingsParams{ClubID: c.ID, Ids: seen, Now: now}); err != nil {
				return err
			}
		}
		var gone []int64
		for _, f := range open {
			switch {
			case present[key{f.Kind, f.SubjectID}]:
			case !checked[f.Kind]:
				openN++ // этот вид проход не перечитывал — находка остаётся открытой
			default:
				gone = append(gone, f.ID)
				if _, err := activity.RecordPlatform(ctx, q, activity.PlatformEntry{
					Event: EventLedgerDriftResolved, Key: msgDriftResolved, ClubID: c.ID,
					Params:  map[string]string{"club": c.Name, "finding": itoa(f.ID)},
					Details: map[string]any{"finding_id": f.ID, "finding_kind": f.Kind, "subject_id": f.SubjectID}, At: now,
				}); err != nil {
					return err
				}
			}
		}
		resolvedN = len(gone)
		if len(gone) > 0 {
			return q.ReconcileResolveFindings(ctx, store.ReconcileResolveFindingsParams{ClubID: c.ID, Ids: gone, Now: now})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("record findings: %w", err)
	}
	res.Found += foundN
	res.Resolved += resolvedN
	res.Open += openN
	return nil
}
