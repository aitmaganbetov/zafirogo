package v1

import (
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Представления v1 — app/Http/Controllers/Api/V1/Transformers.php: только перечисленные поля (без сетевых
// данных ПК, e-mail и полного телефона). Суммы — строкой "500.00" ((string) decimal:2), как в старом API
// (внешний контракт); внутри — тиыны. Даты — toIso8601String() в UTC ("…+00:00").

// iso8601 — Carbon::toIso8601String().
const iso8601 = "2006-01-02T15:04:05-07:00"

func iso(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(iso8601)
	return &s
}

type productView struct {
	ID         int64   `json:"id"`
	SKU        string  `json:"sku"`
	Name       string  `json:"name"`
	CategoryID *int64  `json:"category_id"`
	Category   string  `json:"category"`
	Price      string  `json:"price"`
	Quantity   int64   `json:"quantity"`
	UpdatedAt  *string `json:"updated_at"`
}

func productOf(p store.ApiV1ProductRow) productView {
	return productView{ID: p.ID, SKU: p.Sku, Name: p.Name, CategoryID: p.ProductCategoryID, Category: p.Category,
		Price: money.Tiyn(p.Price).Tenge(), Quantity: p.Quantity, UpdatedAt: iso(p.UpdatedAt)}
}

type categoryView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type movementView struct {
	ID           int64   `json:"id"`
	ProductID    int64   `json:"product_id"`
	Type         string  `json:"type"`
	Quantity     int64   `json:"quantity"`
	BalanceAfter int64   `json:"balance_after"`
	Note         *string `json:"note"`
	CreatedAt    *string `json:"created_at"`
}

func movementOf(m store.ApiV1StockMovementRow) movementView {
	return movementView{ID: m.ID, ProductID: m.ProductID, Type: m.Type, Quantity: m.Quantity, BalanceAfter: m.BalanceAfter,
		Note: m.Note, CreatedAt: iso(m.CreatedAt)}
}

type saleItemView struct {
	Name     string `json:"name"`
	Quantity int64  `json:"quantity"`
	Price    string `json:"price"`
	Total    string `json:"total"`
}

type saleView struct {
	ID          int64          `json:"id"`
	CashShiftID int64          `json:"cash_shift_id"`
	Amount      string         `json:"amount"`
	Items       []saleItemView `json:"items"`
	CreatedAt   *string        `json:"created_at"`
}

// saleOf — Transformers::sale(): строки receipt_data.items (оба формата чека, Q-003); чек без items — [].
func saleOf(s store.ApiV1SaleRow) saleView {
	out := saleView{ID: s.ID, CashShiftID: s.CashShiftID, Amount: money.Tiyn(s.Amount).Tenge(), Items: []saleItemView{},
		CreatedAt: iso(s.CreatedAt)}
	r, err := cashsvc.DecodeReceipt(s.ReceiptData)
	if err != nil || r == nil {
		return out
	}
	for _, l := range r.Items {
		out.Items = append(out.Items, saleItemView{Name: l.Name, Quantity: l.Quantity, Price: l.Price.Tenge(), Total: l.Total.Tenge()})
	}
	return out
}

type zoneView struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	Capacity       *int64  `json:"capacity"`
	Status         string  `json:"status"`
	ComputersCount int64   `json:"computers_count"`
}

type computerView struct {
	ID         int64   `json:"id"`
	ZoneID     int64   `json:"zone_id"`
	Number     int64   `json:"number"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Connection string  `json:"connection"`
	IsReserved bool    `json:"is_reserved"`
	InSession  bool    `json:"in_session"`
	LastSeenAt *string `json:"last_seen_at"`
}

// computerOf — Transformers::computer(): статус и связь — по присутствию агента (heartbeat больше не пишется в
// Postgres на каждый пинг); last_seen_at — из присутствия, иначе последнее записанное в club_computers.
func computerOf(c store.ApiV1ComputerRow, online map[int64]presence.State) computerView {
	st, on := online[c.ID]
	seen := c.LastSeenAt
	if on && !st.LastSeenAt.IsZero() {
		t := st.LastSeenAt
		seen = &t
	}
	m := clubmap.Computer{ID: c.ID, Status: c.Status, IsReserved: c.IsReserved, LastSeenAt: seen, Online: on && st.Online}
	return computerView{ID: c.ID, ZoneID: c.ClubZoneID, Number: c.Number, Name: clubmap.DisplayName(c.DisplayName, c.DeviceName, c.Number),
		Status: m.CurrentStatus(), Connection: m.Connection(), IsReserved: c.IsReserved, InSession: c.CurrentSessionID != nil,
		LastSeenAt: iso(seen)}
}

type tariffView struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	DurationMinutes int64   `json:"duration_minutes"`
	Price           string  `json:"price"`
	Currency        string  `json:"currency"`
	IsActive        bool    `json:"is_active"`
	ZoneIDs         []int64 `json:"zone_ids"`
}

type memberView struct {
	ID           int64   `json:"id"`
	Name         *string `json:"name"`
	PhoneMasked  *string `json:"phone_masked"`
	Role         string  `json:"role"`
	Balance      string  `json:"balance"`
	BonusBalance string  `json:"bonus_balance"`
}

func memberOf(m store.ApiV1MembersRow) memberView {
	phone := ""
	if m.UserPhone != nil {
		phone = *m.UserPhone
	}
	var bal, bonus int64
	if m.Balance != nil {
		bal = *m.Balance
	}
	if m.BonusBalance != nil {
		bonus = *m.BonusBalance
	}
	return memberView{ID: m.UserID, Name: m.UserName, PhoneMasked: maskPhone(phone), Role: m.Role,
		Balance: money.Tiyn(bal).Tenge(), BonusBalance: money.Tiyn(bonus).Tenge()}
}

// maskPhone — Transformers::maskPhone(): +77001234567 → "+7 *** *** 45 67"; меньше 6 цифр — null.
func maskPhone(phone string) *string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(d) < 6 {
		return nil
	}
	s := "+" + d[:1] + " *** *** " + d[len(d)-4:len(d)-2] + " " + d[len(d)-2:]
	return &s
}
