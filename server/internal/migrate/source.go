package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// source — чтение одного клуба из MySQL старой схемы в одном снимке (REPEATABLE READ, CONSISTENT SNAPSHOT):
// все таблицы клуба читаются согласованно, даже если Laravel не заморожен до конца.
type source struct {
	conn *sql.Conn
}

func openSource(ctx context.Context, db *sql.DB) (*source, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("mysql: conn: %w", err)
	}
	// Пояс сессии не меняем: Laravel читал и писал в поясе сервера по умолчанию (config/database.php без
	// 'timezone'), TIMESTAMP-колонки возвращают ту же строку, что записал Laravel (см. parseMySQLTime).
	for _, stmt := range []string{
		"SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ",
		"START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mysql: %s: %w", stmt, err)
		}
	}
	return &source{conn: conn}, nil
}

func (s *source) Close() {
	_, _ = s.conn.ExecContext(context.Background(), "COMMIT")
	_ = s.conn.Close()
}

// Conn — соединение со снимком (для расширений M2).
func (s *source) Conn() *sql.Conn { return s.conn }

// club — переносимый клуб.
type club struct {
	ID       int64
	Slug     string
	Name     string
	Settings *string
}

// resolveClub находит клуб по id или slug.
func (s *source) resolveClub(ctx context.Context, ref string) (club, error) {
	var c club
	var settings sql.NullString
	q := "SELECT id, slug, name, settings FROM clubs WHERE slug = ?"
	arg := any(ref)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil && id > 0 {
		q, arg = "SELECT id, slug, name, settings FROM clubs WHERE id = ?", id
	}
	err := s.conn.QueryRowContext(ctx, q, arg).Scan(&c.ID, &c.Slug, &c.Name, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return club{}, fmt.Errorf("%w: %q", ErrClubNotFound, ref)
	}
	if err != nil {
		return club{}, fmt.Errorf("mysql: club: %w", err)
	}
	if settings.Valid {
		c.Settings = &settings.String
	}
	return c, nil
}

// columns — колонки таблицы MySQL по порядку.
func (s *source) columns(ctx context.Context, tableName string) ([]mysqlColumn, error) {
	rows, err := s.conn.QueryContext(ctx, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, tableName)
	if err != nil {
		return nil, fmt.Errorf("mysql: columns of %s: %w", tableName, err)
	}
	defer rows.Close()
	var out []mysqlColumn
	for rows.Next() {
		var c mysqlColumn
		var nullable string
		if err := rows.Scan(&c.Name, &c.DataType, &c.Type, &nullable); err != nil {
			return nil, err
		}
		c.DataType, c.Type, c.Nullable = strings.ToLower(c.DataType), strings.ToLower(c.Type), nullable == "YES"
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mysql: table %s not found", tableName)
	}
	return out, nil
}

// quoteMy — имя в обратных кавычках MySQL (имена из нашего списка и information_schema).
func quoteMy(name string) string { return "`" + strings.ReplaceAll(name, "`", "``") + "`" }

// scan читает строки таблицы как текст (NULL — nil) и передаёт их fn по одной.
func (s *source) scan(ctx context.Context, tableName string, cols []mysqlColumn, where, orderBy string, args []any,
	fn func(row []*string) error) error {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteMy(c.Name)
	}
	// Таблица, условие и порядок — из coreTables (константы кода), колонки — из information_schema;
	// значения — только параметрами.
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s", // #nosec G201 -- см. выше
		strings.Join(names, ", "), quoteMy(tableName), where, orderBy)
	rows, err := s.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("mysql: read %s: %w", tableName, err)
	}
	defer rows.Close()
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("mysql: scan %s: %w", tableName, err)
		}
		row := make([]*string, len(cols))
		for i, v := range vals {
			if v.Valid {
				str := v.String
				row[i] = &str
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// count — число строк по условию.
func (s *source) count(ctx context.Context, tableName, where string, args []any) (int64, error) {
	var n int64
	err := s.conn.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", quoteMy(tableName), where), args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("mysql: count %s: %w", tableName, err)
	}
	return n, nil
}

// maxID — наибольший id таблицы во всей MySQL (все клубы); 0 — таблица пуста.
func (s *source) maxID(ctx context.Context, tableName string) (int64, error) {
	var n sql.NullInt64
	if err := s.conn.QueryRowContext(ctx, "SELECT MAX(id) FROM "+quoteMy(tableName)).Scan(&n); err != nil {
		return 0, fmt.Errorf("mysql: max id of %s: %w", tableName, err)
	}
	return n.Int64, nil
}

// strings1 — одна строковая колонка результата.
func (s *source) strings1(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v.String)
	}
	return out, rows.Err()
}
