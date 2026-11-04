package customers

import (
	"context"
	"math"
	"strconv"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	customersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// viewCtx — то, что шаблон users.blade.php брал кроме строки клиента: уровни накопительной скидки
// (LoyaltyProgram, настройки клуба) и $staffElsewhere (аккаунты, которые открывают панель).
type viewCtx struct {
	levels []bonus.Level
	locked map[int64]bool
}

// loadView читает уровни лояльности клуба (в транзакции q) и признак «сотрудник другого клуба» для userIDs
// (пул zafiro_platform: роли во всех клубах). Без пула платформы признак не ставится.
func (h *handlers) loadView(ctx context.Context, q *store.Queries, clubID int64, userIDs []int64) (viewCtx, error) {
	v := viewCtx{locked: map[int64]bool{}}
	club, err := q.GetClub(ctx, clubID)
	if err != nil {
		return v, err
	}
	// CurrentClub::settings(): некорректный JSON — настроек нет (накопительной скидки нет).
	if s, err := bonus.ParseSettings(club.Settings); err == nil {
		v.levels = s.LoyaltyLevelsInForce()
	}
	if len(userIDs) == 0 {
		return v, nil
	}
	locked, err := h.accountsLocked(ctx, userIDs)
	if err != nil {
		return v, err
	}
	for _, id := range locked {
		v.locked[id] = true
	}
	return v, nil
}

// accountsLocked — UserController::isAccountLocked() / $staffElsewhere: суперадмин или сотрудник любого клуба.
func (h *handlers) accountsLocked(ctx context.Context, userIDs []int64) ([]int64, error) {
	if h.d.PlatformPool == nil {
		return nil, nil
	}
	var out []int64
	err := db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		out, err = q.AdminAccountsLocked(ctx, userIDs)
		return err
	})
	return out, err
}

// customerRow — общие поля строк клиента из разных запросов.
type customerRow struct {
	UserID       int64
	Name         string
	Phone        string
	Email        *string
	Role         string
	Discount     string
	TotalSpent   int64
	Balance      int64
	BonusBalance int64
}

func (v viewCtx) customer(r customerRow) customersapi.Customer {
	personal, _ := strconv.ParseFloat(strings.TrimSpace(r.Discount), 64)
	loyalty := bonus.LoyaltyPercentFor(v.levels, money.Tiyn(r.TotalSpent))
	return customersapi.Customer{
		UserId: r.UserID, Name: r.Name, Phone: r.Phone, Email: r.Email, Role: r.Role, Discount: r.Discount,
		// users.blade.php: number_format(max((float) $membership->discount, $loyaltyPercent), 2, '.', ' ')
		DiscountEffective: PercentFormat(math.Max(personal, loyalty)),
		// @if ($loyaltyPercent > (float) $membership->discount) «накопительная»
		DiscountCumulative: loyalty > personal,
		AccountLocked:      v.locked[r.UserID],
		TotalSpentTiyn:     r.TotalSpent, BalanceTiyn: r.Balance, BonusBalanceTiyn: r.BonusBalance,
	}
}

// PercentFormat — PHP number_format($x, 2, '.', ' ') для процента (float): округление как PHP (_php_math_round,
// половина — от нуля), группы тысяч через пробел.
func PercentFormat(x float64) string {
	r := money.PHPRound(x, 2)
	s := strconv.FormatFloat(math.Abs(r), 'f', 2, 64)
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	if r < 0 {
		b.WriteByte('-')
	}
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String() + "." + frac
}

func fromGet(r store.AdminGetCustomerRow) customerRow {
	return customerRow{r.UserID, r.Name, r.Phone, r.Email, r.Role, r.Discount, r.TotalSpent, r.Balance, r.BonusBalance}
}

func fromList(r store.AdminListCustomersRow) customerRow {
	return customerRow{r.UserID, r.Name, r.Phone, r.Email, r.Role, r.Discount, r.TotalSpent, r.Balance, r.BonusBalance}
}

func fromListBefore(r store.AdminListCustomersBeforeRow) customerRow {
	return customerRow{r.UserID, r.Name, r.Phone, r.Email, r.Role, r.Discount, r.TotalSpent, r.Balance, r.BonusBalance}
}
