// Команда visual-seed — загрузчик демо-данных визуального сравнения (tools/visual/fixtures/demo.json) в PostgreSQL
// нового сервера. Только для dev/CI: пишет в базу, указанную явно, после `goose up`; базу заранее очищает
// tools/visual/new.sh (отдельная база zafiro_visual, не база разработки).
//
//	go run ./cmd/visual-seed -fixture ../tools/visual/fixtures/demo.json -db "$VISUAL_DATABASE_URL"
//
// DSN — владелец схемы (как у goose): справочники пишутся с явными id (те же id, что в старом Laravel, — одинаковые
// URL страниц). Деньги — по правилам нового сервера:
//   - суммы JSON — строки тенге ("1500.00") → тиыны без float;
//   - начальные балансы клиентов — billing.SetInitialBalance (ledger, операция initial_balance), не UPDATE баланса;
//   - кассовые операции — сервис кассы cash/service с часами = at операции (смены, продажи, пополнения, расходы),
//     как в старом загрузчике сервисом CashRegisterService.
//
// Пароли — bcrypt-хэш старого проекта (login.password_bcrypt): новый сервер принимает его и перехеширует при входе.
// Время журнала действий (activity_logs) ставится равным at операции (журнал пишет time.Now()); время проводок
// ledger и audit_log — время загрузки (таблицы только добавляются).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func main() {
	fixture := flag.String("fixture", "", "путь к demo.json")
	dsn := flag.String("db", os.Getenv("VISUAL_DATABASE_URL"), "DSN владельца схемы (по умолчанию VISUAL_DATABASE_URL)")
	flag.Parse()
	if *fixture == "" || *dsn == "" {
		fmt.Fprintln(os.Stderr, "usage: visual-seed -fixture demo.json -db postgres://owner@host/zafiro_visual")
		os.Exit(2)
	}
	if err := run(context.Background(), *fixture, *dsn); err != nil {
		fmt.Fprintln(os.Stderr, "visual-seed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, fixturePath, dsn string) error {
	raw, err := os.ReadFile(fixturePath) // #nosec G304 -- путь к фикстуре задаёт разработчик (dev-инструмент)
	if err != nil {
		return err
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("demo.json: %w", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("не удалось подключиться к базе (проверьте DSN)")
	}
	defer pool.Close()

	now, err := parseAt(f.Now)
	if err != nil {
		return err
	}
	// Партиции журналов: от самой ранней даты демо-данных и на текущее время загрузки (ledger пишет now()).
	if _, err := db.EnsurePartitions(ctx, pool, now.AddDate(0, -2, 0), 4); err != nil {
		return fmt.Errorf("partitions: %w", err)
	}
	if _, err := db.EnsurePartitions(ctx, pool, time.Now().UTC(), 2); err != nil {
		return fmt.Errorf("partitions: %w", err)
	}

	s := &seeder{f: f, pool: pool, now: now}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"платформа", s.platform},
		{"клубы", s.clubs},
		{"операции кассы", s.operations},
		{"прочее", s.rest},
		{"последовательности", s.sequences},
		{"присутствие агентов", s.presence},
	}
	for _, st := range steps {
		if err := st.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}
	var counts []string
	for _, t := range []string{"users", "club_computers", "tariffs", "products", "cash_shifts", "cash_transactions", "activity_logs", "stock_movements"} {
		var n int64
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
			return err
		}
		counts = append(counts, fmt.Sprintf("%q:%d", t, n))
	}
	fmt.Printf("{\"seeded\":{%s}}\n", strings.Join(counts, ","))
	return nil
}

type seeder struct {
	f    Fixture
	pool *pgxpool.Pool
	now  time.Time
}

// created — время записи: created_at из фикстуры или «сейчас» демо-данных.
func (s *seeder) created(at string) time.Time {
	if t, err := parseAt(at); err == nil && at != "" {
		return t
	}
	return s.now
}

func (s *seeder) platform(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, p := range s.f.SaasPlans {
			price, err := tiynPtr(p.PriceMonthly)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO saas_plans (id, slug, name, price_monthly, trial_days, is_active, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, p.ID, p.Slug, p.Name, price, p.TrialDays, p.IsActive, s.now); err != nil {
				return err
			}
		}
		for _, u := range s.f.Users {
			blocked, err := timePtr(u.BlockedAt)
			if err != nil {
				return err
			}
			c := s.created(u.CreatedAt)
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, name, phone, email, password, is_superadmin, blocked_at, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
				u.ID, u.Name, u.Phone, u.Email, s.f.Login.PasswordBcrypt, u.IsSuperadmin, blocked, c); err != nil {
				return err
			}
		}
		for _, c := range s.f.Clubs {
			at := s.created(c.CreatedAt)
			if _, err := tx.Exec(ctx, `INSERT INTO clubs (id, name, slug, settings, locale, timezone, created_at, updated_at)
				VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $7)`, c.ID, c.Name, c.Slug, jsonOrNil(c.Settings), c.Locale, c.Timezone, at); err != nil {
				return err
			}
			sub := c.Subscription
			started, err := parseAt(sub.StartedAt)
			if err != nil {
				return err
			}
			trial, err := timePtr(sub.TrialEndsAt)
			if err != nil {
				return err
			}
			period, err := timePtr(sub.CurrentPeriodEndsAt)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO club_subscriptions (club_id, saas_plan_id, status, started_at, trial_ends_at,
				current_period_ends_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
				c.ID, sub.SaasPlanID, sub.Status, started, trial, period, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// clubs — справочники клуба и начальные балансы (в транзакции клуба: RLS и проверка тенанта ledger).
func (s *seeder) clubs(ctx context.Context) error {
	users := map[int64]User{}
	for _, u := range s.f.Users {
		users[u.ID] = u
	}
	for _, c := range s.f.Clubs {
		err := s.tenantTx(ctx, c.ID, func(tx pgx.Tx, q *store.Queries) error {
			for _, m := range s.f.Memberships {
				if m.ClubID != c.ID {
					continue
				}
				spent, err := tiyn(m.TotalSpent)
				if err != nil {
					return err
				}
				at := s.created(users[m.UserID].CreatedAt)
				if _, err := tx.Exec(ctx, `INSERT INTO club_memberships (club_id, user_id, role, discount, total_spent,
					can_manage_integrations, permissions, created_at, updated_at)
					VALUES ($1, $2, $3, $4::text::numeric, $5, $6, $7::jsonb, $8, $8)`,
					m.ClubID, m.UserID, m.Role, orDefault(m.Discount, "0.00"), spent, m.CanManageIntegrations,
					jsonOrNil(m.Permissions), at); err != nil {
					return err
				}
				// Строка баланса — с нулями (триггер member_balances_guard), суммы — операцией ledger.
				if _, err := tx.Exec(ctx, `INSERT INTO member_balances (club_id, user_id, balance, bonus_balance, created_at, updated_at)
					VALUES ($1, $2, 0, 0, $3, $3)`, m.ClubID, m.UserID, at); err != nil {
					return err
				}
				main, err := tiyn(m.Balance)
				if err != nil {
					return err
				}
				bonus, err := tiyn(m.BonusBalance)
				if err != nil {
					return err
				}
				if _, err := billing.SetInitialBalance(ctx, q, billing.InitialBalanceParams{
					ClubID: m.ClubID, UserID: m.UserID, Main: main, Bonus: bonus,
					Actor: billing.BalanceActor{Kind: "system"},
				}); err != nil {
					return fmt.Errorf("баланс клиента %d: %w", m.UserID, err)
				}
			}
			return s.catalog(ctx, tx, c.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// tenantTx — транзакция клуба (как db.InTenantTx: app.club_id для RLS и проверки тенанта ledger).
func (s *seeder) tenantTx(ctx context.Context, clubID int64, fn func(tx pgx.Tx, q *store.Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(clubID, 10)); err != nil {
			return err
		}
		return fn(tx, store.New(tx))
	})
}

func (s *seeder) catalog(ctx context.Context, tx pgx.Tx, clubID int64) error {
	for _, z := range s.f.Zones {
		if z.ClubID != clubID {
			continue
		}
		price, err := tiynPtr(z.PricePerHour)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO club_zones (id, club_id, name, description, capacity, icon, theme, status, is_featured,
			sort_order, position_x, position_y, width, height, rotation, price_per_hour, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $17)`,
			z.ID, z.ClubID, z.Name, z.Description, z.Capacity, z.Icon, z.Theme, z.Status, z.IsFeatured, z.SortOrder,
			z.PositionX, z.PositionY, z.Width, z.Height, z.Rotation, price, s.now); err != nil {
			return fmt.Errorf("зона %d: %w", z.ID, err)
		}
	}
	for _, pc := range s.f.Computers {
		if pc.ClubID != clubID {
			continue
		}
		var times [3]*time.Time
		for i, v := range []*string{pc.LastSeenAt, pc.SessionEndsAt, pc.HardwareReportedAt} {
			t, err := timePtr(v)
			if err != nil {
				return err
			}
			times[i] = t
		}
		if _, err := tx.Exec(ctx, `INSERT INTO club_computers (id, club_id, club_zone_id, number, status, is_reserved, position_x,
			position_y, device_name, display_name, last_seen_at, agent_state, agent_version, current_session_id,
			current_session_user_id, session_guest, session_ends_at, mac_address, local_ip, cpu_model, cpu_cores, ram_total_mb,
			gpu_model, os_version, hardware_reported_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
			$25, $26, $26)`,
			pc.ID, pc.ClubID, pc.ZoneID, pc.Number, orDefault(pc.Status, "available"), pc.IsReserved, pc.PositionX, pc.PositionY,
			pc.DeviceName, pc.DisplayName, times[0], pc.AgentState, pc.AgentVersion, pc.CurrentSessionID, pc.CurrentSessionUserID,
			pc.SessionGuest, times[1], pc.MacAddress, pc.LocalIP, pc.CPUModel, pc.CPUCores, pc.RAMTotalMB, pc.GPUModel, pc.OSVersion,
			times[2], s.now); err != nil {
			return fmt.Errorf("ПК %d: %w", pc.ID, err)
		}
	}
	for _, t := range s.f.Tariffs {
		if t.ClubID != clubID {
			continue
		}
		price, err := tiyn(&t.Price)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tariffs (id, club_id, name, description, duration_minutes, price, is_active,
			payment_method, currency, discount_enabled, user_roles, usage_limit_enabled, usage_limit_minutes,
			quantity_limit_enabled, quantity_limit, validity_periods, sale_periods, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'KZT', $9, $10::jsonb, $11, $12, $13, $14, $15::jsonb, $16::jsonb, $17, $17)`,
			t.ID, t.ClubID, t.Name, t.Description, t.DurationMinutes, int64(price), boolOr(t.IsActive, true), t.PaymentMethod,
			t.DiscountEnabled, jsonOrNil(t.UserRoles), t.UsageLimitEnabled, t.UsageLimitMinutes, t.QuantityLimitEnabled,
			t.QuantityLimit, jsonOrNil(t.ValidityPeriods), jsonOrNil(t.SalePeriods), s.now); err != nil {
			return fmt.Errorf("тариф %d: %w", t.ID, err)
		}
		for _, zoneID := range t.ZoneIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO club_zone_tariff (club_zone_id, tariff_id, club_id) VALUES ($1, $2, $3)`,
				zoneID, t.ID, t.ClubID); err != nil {
				return err
			}
		}
	}
	categories := map[int64]string{}
	for _, c := range s.f.ProductCategories {
		if c.ClubID != clubID {
			continue
		}
		categories[c.ID] = c.Name
		if _, err := tx.Exec(ctx, `INSERT INTO product_categories (id, club_id, name, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`,
			c.ID, c.ClubID, c.Name, s.now); err != nil {
			return err
		}
	}
	for _, p := range s.f.Products {
		if p.ClubID != clubID {
			continue
		}
		price, err := tiyn(&p.Price)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO products (id, club_id, product_category_id, category, sku, name, price, quantity,
			created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
			p.ID, p.ClubID, p.CategoryID, categories[p.CategoryID], p.SKU, p.Name, int64(price), p.Quantity, s.now); err != nil {
			return fmt.Errorf("товар %d: %w", p.ID, err)
		}
	}
	for _, sv := range s.f.Services {
		if sv.ClubID != clubID {
			continue
		}
		price, err := tiyn(&sv.Price)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO club_services (id, club_id, name, price, unit, is_active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, sv.ID, sv.ClubID, sv.Name, int64(price), sv.Unit, boolOr(sv.IsActive, true),
			s.now); err != nil {
			return err
		}
	}
	return nil
}

// operations — кассовые операции сервисом кассы нового сервера с часами = at.
func (s *seeder) operations(ctx context.Context) error {
	ctx = activity.WithRequestMeta(ctx, activity.RequestMeta{IP: "127.0.0.1", UserAgent: "Zafiro visual seed"})
	for i, op := range s.f.Operations {
		at, err := parseAt(op.At)
		if err != nil {
			return err
		}
		svc := &cashsvc.Service{Now: func() time.Time { return at }}
		actor := cashsvc.Actor{UserID: op.UserID, Kind: "staff"}
		key := "visual-seed:" + strconv.Itoa(i)
		err = s.tenantTx(ctx, op.ClubID, func(tx pgx.Tx, q *store.Queries) error {
			var lastLog int64
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM activity_logs WHERE club_id = $1`, op.ClubID).Scan(&lastLog); err != nil {
				return err
			}
			if err := s.operation(ctx, q, svc, actor, key, op); err != nil {
				return err
			}
			// Журнал действий пишет time.Now(): время записи = время операции, как у старого кода.
			_, err := tx.Exec(ctx, `UPDATE activity_logs SET created_at = $3, updated_at = $3 WHERE club_id = $1 AND id > $2`,
				op.ClubID, lastLog, at)
			return err
		})
		if err != nil {
			if ce, ok := cash.AsError(err); ok {
				return fmt.Errorf("операция #%d (%s %s): %s: %s", i, op.Op, op.At, ce.Field, ce.Text())
			}
			return fmt.Errorf("операция #%d (%s %s): %w", i, op.Op, op.At, err)
		}
	}
	return nil
}

func (s *seeder) operation(ctx context.Context, q *store.Queries, svc *cashsvc.Service, actor cashsvc.Actor, key string, op Operation) error {
	switch op.Op {
	case "open_shift":
		amount, err := cash.ParseOpeningBalance(op.OpeningBalance)
		if err != nil {
			return err
		}
		_, err = svc.OpenShift(ctx, q, cashsvc.OpenShiftParams{ClubID: op.ClubID, OpeningBalance: amount, Actor: actor})
		return err
	case "sale":
		items := make([]cash.SaleItem, len(op.Items))
		for i, it := range op.Items {
			items[i] = cash.SaleItem{ProductID: it.ProductID, ServiceID: it.ServiceID, Quantity: it.Quantity}
		}
		_, err := svc.Sale(ctx, q, cashsvc.SaleParams{ClubID: op.ClubID, Items: items, IdempotencyKey: key, Actor: actor})
		return err
	case "top_up":
		amount, err := cash.ParseTopUpAmount(op.Amount)
		if err != nil {
			return err
		}
		_, err = svc.TopUp(ctx, q, cashsvc.TopUpParams{
			ClubID: op.ClubID, UserID: op.CustomerID, Field: cash.BalanceField(op.BalanceType), Amount: amount,
			PaymentMethod: cash.PaymentMethod(op.PaymentMethod), IdempotencyKey: key, Actor: actor,
		})
		return err
	case "transaction":
		amount, err := cash.ParseTransactionAmount(op.Amount)
		if err != nil {
			return err
		}
		_, err = svc.Transact(ctx, q, cashsvc.TransactParams{
			ClubID: op.ClubID, Type: cash.Type(op.Type), Amount: amount, Description: op.Description, IdempotencyKey: key, Actor: actor,
		})
		return err
	case "close_shift":
		shift, err := cashsvc.LockOpenShift(ctx, q, op.ClubID)
		if err != nil {
			return err
		}
		if shift == nil {
			return errors.New("нет открытой смены")
		}
		// Как старый CashRegisterService::close() (загрузчик старого вызывает сервис, не контроллер): без отметки печати Z.
		_, err = svc.CloseShift(ctx, q, cashsvc.CloseShiftParams{ClubID: op.ClubID, ShiftID: shift.ID, Actor: actor})
		return err
	}
	return fmt.Errorf("неизвестная операция %q", op.Op)
}

//nolint:misspell // колонка старой схемы
const insertReservation = `INSERT INTO computer_reservations (id, club_id, club_computer_id, user_id, customer_name,
	customer_phone, note, starts_at, ends_at, arrived_at, cancelled_at, created_by, created_at, updated_at)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`

// rest — заказы, брони, новости, задачи, чат, журнал (без движения денег: заказы с ПК — готовые записи).
func (s *seeder) rest(ctx context.Context) error {
	for _, c := range s.f.Clubs {
		err := s.tenantTx(ctx, c.ID, func(tx pgx.Tx, q *store.Queries) error {
			for _, o := range s.f.ShopOrders {
				if o.ClubID != c.ID {
					continue
				}
				lines := make([]shop.Line, len(o.Items))
				for i, it := range o.Items {
					price, err := tiyn(&it.Price)
					if err != nil {
						return err
					}
					lines[i] = shop.Line{ProductID: it.ProductID, Name: it.Name, Quantity: it.Quantity, Price: price,
						Total: price * money.Tiyn(it.Quantity)}
				}
				items, err := shop.EncodeItems(lines)
				if err != nil {
					return err
				}
				amount, err := tiyn(&o.Amount)
				if err != nil {
					return err
				}
				handled, err := timePtr(o.HandledAt)
				if err != nil {
					return err
				}
				updated := s.created(o.CreatedAt)
				if handled != nil {
					updated = *handled
				}
				if _, err := tx.Exec(ctx, `INSERT INTO shop_orders (id, club_id, user_id, club_computer_id, computer_name, items, amount,
					status, handled_by, handled_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
					o.ID, o.ClubID, o.UserID, o.ComputerID, o.ComputerName, items, int64(amount), o.Status, o.HandledBy, handled,
					s.created(o.CreatedAt), updated); err != nil {
					return fmt.Errorf("заказ %d: %w", o.ID, err)
				}
			}
			for _, r := range s.f.Reservations {
				if r.ClubID != c.ID {
					continue
				}
				var ts [4]*time.Time
				for i, v := range []*string{&r.StartsAt, &r.EndsAt, r.ArrivedAt, r.CancelledAt} {
					t, err := timePtr(v)
					if err != nil {
						return err
					}
					ts[i] = t
				}
				if _, err := tx.Exec(ctx, insertReservation,
					r.ID, r.ClubID, r.ComputerID, r.UserID, r.CustomerName, r.CustomerPhone, r.Note, ts[0], ts[1], ts[2], ts[3],
					r.CreatedBy, s.created(r.CreatedAt)); err != nil {
					return fmt.Errorf("бронь %d: %w", r.ID, err)
				}
			}
			for _, t := range s.f.TodoTasks {
				if t.ClubID != c.ID {
					continue
				}
				done, err := timePtr(t.DoneAt)
				if err != nil {
					return err
				}
				updated := s.created(t.CreatedAt)
				if done != nil {
					updated = *done
				}
				if _, err := tx.Exec(ctx, `INSERT INTO todo_tasks (id, club_id, title, due_on, created_by, done_at, done_by, created_at,
					updated_at) VALUES ($1, $2, $3, $4::date, $5, $6, $7, $8, $9)`,
					t.ID, t.ClubID, t.Title, t.DueOn, t.CreatedBy, done, t.DoneBy, s.created(t.CreatedAt), updated); err != nil {
					return fmt.Errorf("задача %d: %w", t.ID, err)
				}
			}
			for _, m := range s.f.ChatMessages {
				if m.ClubID != c.ID {
					continue
				}
				read, err := timePtr(m.ReadAt)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO chat_messages (id, club_id, customer_id, author_id, from_staff, body,
					club_computer_id, read_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`,
					m.ID, m.ClubID, m.CustomerID, m.AuthorID, m.FromStaff, m.Body, m.ComputerID, read, s.created(m.CreatedAt)); err != nil {
					return fmt.Errorf("сообщение %d: %w", m.ID, err)
				}
			}
			for _, l := range s.f.QrLogins {
				if l.ClubID != c.ID {
					continue
				}
				expires, err := time.Parse(time.RFC3339, l.ExpiresAt)
				if err != nil {
					return fmt.Errorf("QR-вход %s: %w", l.ID, err)
				}
				sum := sha256.Sum256([]byte(l.Code)) // AgentQrLogin::hashCode(): hash('sha256', $code)
				if _, err := tx.Exec(ctx, `INSERT INTO agent_qr_logins (id, club_id, club_computer_id, code_hash, expires_at,
					created_at, updated_at) VALUES ($1::uuid, $2, $3, $4, $5, $6, $6)`,
					l.ID, l.ClubID, l.ComputerID, hex.EncodeToString(sum[:]), expires, s.created(l.CreatedAt)); err != nil {
					return fmt.Errorf("QR-вход %s: %w", l.ID, err)
				}
			}
			for _, l := range s.f.ActivityLogs {
				if l.ClubID != c.ID {
					continue
				}
				var subjectType *string
				if l.SubjectUserID != nil {
					st := `App\Models\User`
					subjectType = &st
				}
				if _, err := tx.Exec(ctx, `INSERT INTO activity_logs (club_id, event, description, user_id, subject_type, subject_id,
					details, ip_address, user_agent, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, '127.0.0.1', 'Zafiro visual seed', $8, $8)`,
					l.ClubID, l.Event, l.Description, l.UserID, subjectType, l.SubjectUserID, jsonOrNil(l.Details),
					s.created(l.CreatedAt)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Новости: club_id NULL — новость платформы (вне транзакции клуба: политика tenant_insert требует свой клуб).
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, n := range s.f.News {
			if _, err := tx.Exec(ctx, `INSERT INTO news (id, club_id, title, body, is_published, created_by, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, n.ID, n.ClubID, n.Title, n.Body, n.IsPublished, n.CreatedBy,
				s.created(n.CreatedAt)); err != nil {
				return fmt.Errorf("новость %d: %w", n.ID, err)
			}
		}
		// Журнал платформы (PlatformLogger): вне клуба, как новости платформы.
		for _, l := range s.f.PlatformLogs {
			if _, err := tx.Exec(ctx, `INSERT INTO platform_logs (id, user_id, club_id, event, description, ip_address, user_agent,
				created_at, updated_at) VALUES ($1, $2, $3, $4, $5, '127.0.0.1', 'Zafiro visual seed', $6, $6)`,
				l.ID, l.UserID, l.ClubID, l.Event, l.Description, s.created(l.CreatedAt)); err != nil {
				return fmt.Errorf("журнал платформы %d: %w", l.ID, err)
			}
		}
		return nil
	})
}

// sequences — identity-последовательности после вставки с явными id (иначе следующая вставка сервера получит занятый id).
func (s *seeder) sequences(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT c.table_name, c.column_name FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
		WHERE c.table_schema = 'public' AND c.is_identity = 'YES'`)
	if err != nil {
		return err
	}
	type col struct{ table, column string }
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (col, error) {
		var c col
		return c, r.Scan(&c.table, &c.column)
	})
	if err != nil {
		return err
	}
	for _, c := range cols {
		tbl, column := pgx.Identifier{c.table}.Sanitize(), pgx.Identifier{c.column}.Sanitize()
		// setval(seq, max, true): следующий id = max + 1; пустая таблица — 1 (is_called = false).
		//nolint:gosec // имена таблиц и колонок — из information_schema, экранированы pgx.Identifier
		q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence($1, $2), coalesce((SELECT max(%s) FROM %s), 1),
			(SELECT max(%s) FROM %s) IS NOT NULL)`, column, tbl, column, tbl)
		if _, err := s.pool.Exec(ctx, q, c.table, c.column); err != nil {
			slog.Warn("sequence", "table", c.table, "err", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Значения
// ---------------------------------------------------------------------------

// parseAt — время фикстуры (RFC 3339 с поясом клуба).
func parseAt(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("время %q: ожидается RFC 3339", s)
	}
	return t, nil
}

func timePtr(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := parseAt(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// tiyn — сумма тенге строкой ("1500.00", "80", "15750.5") → тиыны без float; nil — 0.
func tiyn(s *string) (money.Tiyn, error) {
	if s == nil || *s == "" {
		return 0, nil
	}
	v := *s
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimPrefix(v, "-")
	whole, frac, _ := strings.Cut(v, ".")
	if len(frac) > 2 || whole == "" {
		return 0, fmt.Errorf("сумма %q: ожидается тенге с не более чем двумя знаками", *s)
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("сумма %q: %w", *s, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("сумма %q: %w", *s, err)
	}
	t := money.Tiyn(w*100 + f)
	if neg {
		t = -t
	}
	return t, t.Check()
}

func tiynPtr(s *string) (*int64, error) {
	if s == nil {
		return nil, nil
	}
	t, err := tiyn(s)
	if err != nil {
		return nil, err
	}
	v := int64(t)
	return &v, nil
}

func jsonOrNil(v json.RawMessage) *string {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	s := string(v)
	return &s
}

func orDefault(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}
