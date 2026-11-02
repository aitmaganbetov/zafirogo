// Package migrate — поклубный перенос данных из MySQL старого Laravel-проекта Zafiro в Postgres zafirogo
// (команда server/cmd/migrate-from-mysql, описание — tools/migrate-from-mysql/README.md).
//
// Ядро (Этап 5, M1):
//   - Plan — сухой прогон: все строки клуба читаются и преобразуются так же, как в Apply; отчёт о проблемах
//     данных (Q-004, Q-022/Q-023, Q-025, Q-026, Q-027, Q-030) и конфликтах с уже перенесёнными клубами;
//   - Apply — одна транзакция Postgres на клуб: таблицы клуба с сохранением id (Q-029), деньги decimal → тиыны
//     (× 100 через строку), время → timestamptz, JSON → jsonb (формат v1), входящие остатки через ledger
//     (billing.MigrateOpeningBalance), партиции журналов под старые даты, сдвиг последовательностей, сверка до
//     коммита. Повторный запуск для перенесённого клуба — ErrAlreadyMigrated, ничего не меняется;
//   - Verify — сверка после переноса (число строк, балансы, total_spent, склад, касса, ПК, ledger).
//
// Производные данные (JSON v2, tariff_sales, income_kind, zone_id/source, токены и ключи, секреты APP_KEY,
// файлы) — расширения (Extension, M2).
package migrate
