package service

// Что пакету броней (server/internal/reservations: брони из гостевого приложения, тема Г docs/upstream-2026-11.md)
// нужно из сервисного слоя покупок: те же правила клуба, тот же разбор строки тарифа, тот же id сессии и то же
// событие ПК, что у покупки времени. Сам сервис броней живёт в reservations — пакет club (его клуб запроса)
// зависит от этого пакета, поэтому reservations импортирует service, а не наоборот.

import (
	"context"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ClubRules — то, что старые сервисы читали из CurrentClub для расчётов (см. clubContext).
type ClubRules struct {
	Rules pricing.PaymentRules
	Bonus bonus.Settings
	// Loc — CurrentClub::localTimezone(): периоды тарифа и времена в текстах — местное время клуба.
	Loc        *time.Location
	GapMinutes int64
	Currency   string
}

// LoadClubRules читает правила клуба в тенантной транзакции (тот же разбор, что у покупки времени: неизвестный
// часовой пояс — ошибка, а не молчаливый UTC).
func LoadClubRules(ctx context.Context, q *store.Queries, clubID int64) (ClubRules, error) {
	c, err := loadClub(ctx, q, clubID)
	if err != nil {
		return ClubRules{}, err
	}
	return ClubRules{Rules: c.rules, Bonus: c.bonus, Loc: c.loc, GapMinutes: c.gapMinutes, Currency: c.currency}, nil
}

// TariffFields — строка tariffs для расчётов (как её читают LockTariffForPurchase и списки тарифов).
type TariffFields = tariffRow

// PricingTariff переводит строку тарифа и его зоны во входные данные pricing (tariffRow.toPricing).
func PricingTariff(t TariffFields, zoneIDs []int64) (pricing.Tariff, error) {
	return t.toPricing(zoneIDs)
}

// NewSessionID — (string) Str::uuid(): id новой сессии ПК.
func NewSessionID() string { return newUUIDv4() }

// ComputerStateEvent — data события computer.state после начала или продления сессии (computerEvent).
func ComputerStateEvent(computerID int64, sessionID string, userID int64, guest bool, endsAt *time.Time) map[string]any {
	return computerEvent(computerID, sessionID, userID, guest, endsAt)
}
