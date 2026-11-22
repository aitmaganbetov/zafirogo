package session

import "time"

// TariffUsage — поля тарифа, от которых зависит срок сохранения остатка (минуты тарифа считает
// pricing.Tariff.EffectiveMinutes, запуск пакета — pricing.QuoteStartPackage).
type TariffUsage struct {
	DurationMinutes   int64
	UsageLimitEnabled bool
	UsageLimitMinutes *int64
}

// TariffPartExpiresAt — expires_at части за тариф: до него остаток сохраняется пакетом (BR-015).
// nil — тариф без лимита использования (остаток при досрочном завершении сгорает).
// Laravel: AgentTariffPurchaseService::purchase() стр. 119 — now()->addMinutes(usage_limit_minutes),
// сохраняется усечённым до секунды.
func TariffPartExpiresAt(t TariffUsage, now time.Time) *time.Time {
	if !t.UsageLimitEnabled || t.UsageLimitMinutes == nil {
		return nil
	}
	e := StoredTime(now.Add(time.Duration(*t.UsageLimitMinutes) * time.Minute))
	return &e
}
