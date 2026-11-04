package migrate

import (
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
)

// DefaultExtensions — все расширения M2 в порядке выполнения (после ядра, в той же транзакции):
// JSON v2 и настройки (Q-003, Q-023, CQ-9), производные данные из журнала (Q-007, CSQ-2, SV-3), интеграции
// (ключи API, вебхуки с секретами Q-050, токены мобильных приложений), файлы картинок (Q-085).
func DefaultExtensions() []Extension {
	return []Extension{JSONv2(), Derived(), Integrations(), Files()}
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) }
