// Package activity — журнал действий клуба (activity_logs): кто, что, с какого IP и браузера.
//
// Laravel: app/Services/ActivityLogger.php ActivityLogger::record() / recordFailedLogin().
// Запись делается в той же транзакции, что и само действие (q — из db.InTenantTx): откат действия
// откатывает и запись журнала, как DB::transaction() вокруг record() в старом коде.
//
// Формат строки — как в старом: event ("cash.income"), description (готовый текст на языке клуба —
// старый код сохранял результат __()), user_id, subject_type/subject_id (морф-связь Eloquent,
// "App\Models\ClubComputer" — так лежат перенесённые строки), details (JSON, NULL если пусто),
// ip_address (обязателен), user_agent. Для действий через API-ключ в details.api_key пишется публичный
// префикс ключа (ActivityLogger::record() ~стр. 36).
//
// Отличие от старого: details проходят фильтр секретов (Redact) — поля с паролями, токенами, PIN-кодами
// и т. п. не попадают в журнал (CLAUDE.md, «Безопасность»). Старый код такие поля не писал; фильтр —
// страховка от ошибок при переносе.
//
// activity_logs — журнал интерфейса (история, отчёты, колокольчик уведомлений); аудит денег, тарифов и прав —
// отдельная таблица audit_log (0003_audit.sql), её пишет другой код.
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Ограничения колонок activity_logs (0001_init.sql).
const (
	maxEvent       = 60
	maxSubjectType = 100
	maxIP          = 45
)

var (
	// ErrNoIP — как LogicException старого кода: «Activity log entries require the client IP address.»
	ErrNoIP = errors.New("activity: activity log entries require the client IP address")
	// ErrInvalid — пустые обязательные поля или превышена длина колонки.
	ErrInvalid = errors.New("activity: invalid entry")
)

// RequestMeta — данные запроса, которые журнал берёт «из request()» в старом коде.
type RequestMeta struct {
	// IP — адрес клиента (после разбора доверенных прокси в HTTP-слое). Обязателен.
	IP string
	// UserAgent — заголовок User-Agent; пусто — NULL.
	UserAgent string
	// APIKeyPrefix — публичный префикс API-ключа, если действие сделано интеграцией (только префикс!).
	APIKeyPrefix string
}

type metaKey struct{}

// WithRequestMeta кладёт данные запроса в контекст (middleware HTTP/gRPC — один раз на запрос).
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// RequestMetaFromContext достаёт данные запроса.
func RequestMetaFromContext(ctx context.Context) (RequestMeta, bool) {
	m, ok := ctx.Value(metaKey{}).(RequestMeta)
	return m, ok
}

// Entry — одна запись журнала.
type Entry struct {
	// ClubID — клуб записи. Должен совпадать с клубом транзакции (RLS).
	ClubID int64
	// Event — код события ("cash.income"); список — ActivityLog::EVENT_LABELS старого кода.
	Event string
	// Description — готовый текст (уже переведённый на язык клуба, с подстановками).
	Description string
	// UserID — кто сделал (член клуба: FK (club_id, user_id) → club_memberships).
	UserID int64
	// SubjectType — морф-класс предмета ("App\Models\User"); пусто — без предмета.
	SubjectType string
	// SubjectID — id предмета (0 — без предмета).
	SubjectID int64
	// Details — подробности (значения — строки/числа/списки; суммы — в тенге, как в старом журнале).
	Details map[string]any
	// Meta — данные запроса; пусто — берутся из контекста (WithRequestMeta).
	Meta *RequestMeta
	// At — время записи (нулевое — time.Now()).
	At time.Time
}

// Record пишет запись в activity_logs в транзакции q и возвращает её id.
// Laravel: app/Services/ActivityLogger.php ActivityLogger::record().
func Record(ctx context.Context, q *store.Queries, e Entry) (int64, error) {
	params, err := build(ctx, e)
	if err != nil {
		return 0, err
	}
	id, err := q.InsertActivityLog(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("activity: insert %s: %w", e.Event, err)
	}
	if err := notifyBell(ctx, q, e); err != nil {
		return 0, err
	}
	return id, nil
}

// RecordInClubs пишет одну и ту же запись в каждый клуб clubIDs — как recordFailedLogin(): неудачный вход
// журналируется во всех клубах аккаунта. Транзакция q должна видеть все эти клубы (пул zafiro_platform);
// под ролью zafiro_app с одним app.club_id вставка в чужой клуб отклоняется RLS.
// Пустой clubIDs — ничего не пишет (старый код в этом случае писал только в системный лог).
func RecordInClubs(ctx context.Context, q *store.Queries, clubIDs []int64, e Entry) error {
	for _, clubID := range clubIDs {
		e.ClubID = clubID
		if _, err := Record(ctx, q, e); err != nil {
			return err
		}
	}
	return nil
}

func build(ctx context.Context, e Entry) (store.InsertActivityLogParams, error) {
	var meta RequestMeta
	if e.Meta != nil {
		meta = *e.Meta
	} else if m, ok := RequestMetaFromContext(ctx); ok {
		meta = m
	}
	if meta.IP == "" {
		return store.InsertActivityLogParams{}, ErrNoIP
	}
	switch {
	case e.ClubID <= 0, e.UserID <= 0, e.Event == "", e.Description == "":
		return store.InsertActivityLogParams{}, fmt.Errorf("%w: club, user, event and description are required", ErrInvalid)
	case utf8.RuneCountInString(e.Event) > maxEvent,
		utf8.RuneCountInString(e.SubjectType) > maxSubjectType,
		utf8.RuneCountInString(meta.IP) > maxIP:
		return store.InsertActivityLogParams{}, fmt.Errorf("%w: value too long", ErrInvalid)
	case e.SubjectID < 0:
		return store.InsertActivityLogParams{}, fmt.Errorf("%w: negative subject id", ErrInvalid)
	}

	details, removed := Redact(e.Details)
	if len(removed) > 0 {
		// Имена полей — не значения.
		slog.WarnContext(ctx, "activity: secret fields removed from details", "event", e.Event, "fields", removed)
	}
	if meta.APIKeyPrefix != "" {
		if details == nil {
			details = map[string]any{}
		}
		details["api_key"] = meta.APIKeyPrefix
	}
	var detailsJSON json.RawMessage
	if len(details) > 0 { // 'details' => $details ?: null
		b, err := json.Marshal(details)
		if err != nil {
			return store.InsertActivityLogParams{}, fmt.Errorf("activity: details: %w", err)
		}
		detailsJSON = b
	}

	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	p := store.InsertActivityLogParams{
		ClubID:      e.ClubID,
		Event:       e.Event,
		Description: e.Description,
		UserID:      e.UserID,
		Details:     detailsJSON,
		IpAddress:   meta.IP,
		CreatedAt:   at,
	}
	if e.SubjectType != "" && e.SubjectID > 0 {
		st, sid := e.SubjectType, e.SubjectID
		p.SubjectType, p.SubjectID = &st, &sid
	}
	if meta.UserAgent != "" {
		ua := meta.UserAgent
		p.UserAgent = &ua
	}
	return p, nil
}

// secretWords — части имён полей, значения которых в журнал не пишутся (сравнение без регистра).
var secretWords = []string{
	"password", "passwd", "secret", "token", "otp", "cvv", "card_number", "authorization", "cookie",
	"private_key", "plain_key", "api_key_plain", "recovery_code", "verification_code", "installation_key",
}

// IsSecretField — похоже ли имя поля на секрет: содержит одно из secretWords или одно из его слов —
// "pin" ("pin", "exit_pin", "pin_hash", "agentPin").
func IsSecretField(name string) bool {
	n := strings.ToLower(name)
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return slices.Contains(words(name), "pin")
}

// words делит имя поля на слова по '_', '-', '.', пробелам и границам camelCase, в нижнем регистре.
func words(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.' || unicode.IsSpace(r):
			flush()
		case unicode.IsUpper(r):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// Redact возвращает копию details без полей-секретов (рекурсивно во вложенных объектах и списках) и
// пути удалённых полей. Входная карта не меняется.
func Redact(details map[string]any) (map[string]any, []string) {
	if details == nil {
		return nil, nil
	}
	var removed []string
	out := redactMap(details, "", &removed)
	return out, removed
}

func redactMap(m map[string]any, prefix string, removed *[]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if IsSecretField(k) {
			*removed = append(*removed, path)
			continue
		}
		out[k] = redactValue(v, path, removed)
	}
	return out
}

func redactValue(v any, path string, removed *[]string) any {
	switch t := v.(type) {
	case map[string]any:
		return redactMap(t, path, removed)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactValue(item, path, removed)
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactMap(item, path, removed)
		}
		return out
	default:
		return v
	}
}
