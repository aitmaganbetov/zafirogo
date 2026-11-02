// Package migrate — поклубный перенос данных из MySQL старого Laravel-проекта Zafiro в Postgres zafirogo
// (команда server/cmd/migrate-from-mysql, описание — tools/migrate-from-mysql/README.md).
//
// Ядро (Этап 5, M1):
//   - Plan — сухой прогон: все строки клуба читаются и преобразуются так же, как в Apply; отчёт о проблемах
//     данных (Q-004, Q-022/Q-023, Q-025, Q-026, Q-027, Q-030) и конфликтах с уже перенесёнными клубами;
//   - Apply — одна транзакция Postgres на клуб: таблицы клуба с сохранением id (Q-029), деньги decimal → тиыны
//     (× 100 через строку), время → timestamptz, JSON → jsonb (формат v1), входящие остатки через ledger
//     (postOpeningBalances: migration_opening_balance по 500 клиентов), партиции журналов под старые даты, сдвиг последовательностей, сверка до
//     коммита. Повторный запуск для перенесённого клуба — ErrAlreadyMigrated, ничего не меняется;
//   - Verify — сверка после переноса (число строк, балансы, total_spent, склад, касса, ПК, ledger).
//
// Производные данные (M2) — расширения (Extension, DefaultExtensions) в той же транзакции и сверке:
//   - JSONv2 — receipt_data, shop_orders.items, уровни и нормализация clubs.settings (Q-003, Q-023, BQ-3, CQ-9);
//   - Derived — tariff_sales (Q-007), cash_transactions.income_kind/payment_method (CSQ-2, Q-013),
//     session_purchases.zone_id/source идущих сессий (SV-3) из activity_logs;
//   - Integrations — api_keys, api_idempotency_keys, webhook_endpoints (секрет: LaravelKeys → secretbox, Q-050),
//     webhook_deliveries, personal_access_tokens → mobile_api_tokens;
//   - Files — логотип, картинка входа, картинки новостей с диска старого Laravel (Q-085).
//
// Платформенный шаг (PlatformPlan/PlatformApply/PlatformVerify) — один раз перед первым клубом: языки, переводы,
// platform_settings (токены Telegram — Q-050), суперадмины, новости и журнал платформы, тарифы платформы.
//
// Состояние клуба (M3, 0027): Apply оставляет клуб в состоянии imported (подписка закрыта); GoLive открывает его
// (live, окончательно); Purge до этого удаляет клуб из Postgres для повторного переноса (zafiro_purge_imported_club).
// Plan/Apply/Verify пересчитывают балансы по истории старой базы (Q-005, history.go) — предупреждением.
package migrate
