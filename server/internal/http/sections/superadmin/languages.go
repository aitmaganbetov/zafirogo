package superadmin

// Языки и переводы (общие для всех клубов). Laravel: app/Http/Controllers/Superadmin/LanguageController.php,
// app/Services/TranslationCatalog.php, TranslationTransfer.php.
//
// Исходный каталог (TranslationCatalog::sourceKeys() — database/translations/ru.php) в новом сервере — строки языка
// ru в таблице translations (их загружает cmd/import-translations из того же файла). Строки хранятся в формате
// Laravel (:name) — его и показывает редактор (Q-065); в формат i18next их переводит каталог при выдаче пакета
// (GET /api/i18n/{locale}, ETag меняется вместе с версией). Любая правка сдвигает версию переводов (PD-12:
// updated_at строк и языка), агентам на связи уходит TranslationsChanged.

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	defaultClubLocale = "ru" // LocaleResolver::DEFAULT_CLUB_LOCALE
	translationsPage  = 50
	maxSavePerPage    = 200  // translations max:200
	maxTranslation    = 5000 // translations.* max:5000
	maxImportBytes    = 2048 * 1024
	maxImportRows     = 10000 // TranslationTransfer::MAX_ROWS
	subjectLanguage   = `App\Models\Language`
)

var (
	langCodeRe    = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)
	placeholderRe = regexp.MustCompile(`(?i):[a-z_]+`)
	formulaRe     = regexp.MustCompile(`^[=+\-@\t\r]`)
	protectedRe   = regexp.MustCompile(`^'[=+\-@\t\r]`)
)

// source — строка исходного каталога: группа, ключ, русский текст.
type source struct{ group, key, text string }

// sources — TranslationTransfer::sources(): хэш → строка, в порядке каталога.
func sources(ctx context.Context, q *store.Queries) (map[string]source, []string, error) {
	rows, err := q.SuperadminSourceStrings(ctx)
	if err != nil {
		return nil, nil, err
	}
	m := make(map[string]source, len(rows))
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, dup := m[r.KeyHash]; dup {
			continue
		}
		m[r.KeyHash] = source{group: r.Grp, key: r.Key, text: r.Value}
		order = append(order, r.KeyHash)
	}
	return m, order, nil
}

// SaListLanguages — LanguageController::index().
func (h *handlers) SaListLanguages(ctx context.Context, _ superadminapi.SaListLanguagesRequestObject) (superadminapi.SaListLanguagesResponseObject, error) {
	out := superadminapi.LanguageList{Items: []superadminapi.Language{}}
	err := h.tx(ctx, func(q *store.Queries) error {
		langs, err := q.SuperadminLanguages(ctx)
		if err != nil {
			return err
		}
		src, _, err := sources(ctx, q)
		if err != nil {
			return err
		}
		out.TranslationTotal = int64(len(src))
		for _, l := range langs {
			out.Items = append(out.Items, superadminapi.Language{Id: l.ID, Code: l.Code, Name: l.Name, IsEnabled: l.IsEnabled,
				IsSystem: l.IsSystem, IsDefault: l.Code == defaultClubLocale, TranslatedCount: l.TranslatedCount, ClubsCount: l.ClubsCount})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListLanguages200JSONResponse(out), nil
}

// SaCreateLanguage — store().
func (h *handlers) SaCreateLanguage(ctx context.Context, req superadminapi.SaCreateLanguageRequestObject) (superadminapi.SaCreateLanguageResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("code", "required")
	}
	code, name := strings.TrimSpace(req.Body.Code), strings.TrimSpace(req.Body.Name)
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		var fe httpx.FormErrors
		switch {
		case code == "":
			fe.Add("code", httpx.RuleMessage("code", "required", ""))
		case len(code) > 16:
			fe.Add("code", httpx.RuleMessage("code", "max_string", "16"))
		case !langCodeRe.MatchString(code):
			fe.AddMsg("code", "Код языка: 2–3 латинские буквы, например uz или tr.", nil)
		default:
			taken, err := q.SuperadminLanguageCodeTaken(ctx, code)
			if err != nil {
				return err
			}
			if taken {
				fe.Add("code", httpx.RuleMessage("code", "unique", ""))
			}
		}
		switch {
		case name == "":
			fe.Add("name", httpx.RuleMessage("name", "required", ""))
		case utf8.RuneCountInString(name) > 64:
			fe.Add("name", httpx.RuleMessage("name", "max_string", "64"))
		}
		if err := fe.Err(); err != nil {
			return err
		}
		id, err := q.SuperadminInsertLanguage(ctx, store.SuperadminInsertLanguageParams{Code: code, Name: name, Now: &now})
		if err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "language.created", Key: "Добавлен язык: :name (:code)",
			Params: map[string]string{"name": name, "code": code}, SubjectType: subjectLanguage, SubjectID: id}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "language.created", subjectLanguage, id, nil, map[string]any{"code": code, "name": name})
	})
	if err != nil {
		return nil, err
	}
	h.translationsChanged(ctx)
	return superadminapi.SaCreateLanguage201JSONResponse(msg(h.d.T(ctx, "Язык добавлен. Теперь заполните переводы.", nil))), nil
}

// SaUpdateLanguage — update(): имя и «показывать»; язык клуба и последний включённый не выключаются.
func (h *handlers) SaUpdateLanguage(ctx context.Context, req superadminapi.SaUpdateLanguageRequestObject) (superadminapi.SaUpdateLanguageResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	now := h.d.NowFunc()
	changed := false
	err := h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLockLanguage(ctx, req.LanguageId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		name, enabled := l.Name, l.IsEnabled
		if req.Body.Name != nil {
			name = strings.TrimSpace(*req.Body.Name)
			switch {
			case name == "":
				return httpx.RuleError("name", "required")
			case utf8.RuneCountInString(name) > 64:
				return httpx.RuleErrorWith("name", "max_string", "64")
			}
		}
		if req.Body.IsEnabled != nil {
			enabled = *req.Body.IsEnabled
			if !enabled {
				used, err := q.SuperadminLanguageClubsExist(ctx, &l.Code)
				if err != nil {
					return err
				}
				if l.Code == defaultClubLocale || used {
					return httpx.ValidationMsg("language", "Нельзя выключить язык, который выбран для клуба.", nil)
				}
				other, err := q.SuperadminOtherEnabledLanguageExists(ctx, l.ID)
				if err != nil {
					return err
				}
				if !other {
					return httpx.ValidationMsg("language", "Должен остаться хотя бы один включённый язык.", nil)
				}
			}
		}
		changes := map[string]any{}
		if name != l.Name {
			changes["name"] = name
		}
		if enabled != l.IsEnabled {
			changes["is_enabled"] = map[bool]string{true: "1", false: ""}[enabled]
		}
		if len(changes) == 0 {
			return nil
		}
		changed = true
		if err := q.SuperadminUpdateLanguage(ctx, store.SuperadminUpdateLanguageParams{Name: name, IsEnabled: enabled, Now: &now, ID: l.ID}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "language.updated", Key: "Изменён язык: :name (:code)",
			Params: map[string]string{"name": name, "code": l.Code}, SubjectType: subjectLanguage, SubjectID: l.ID, Details: changes}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "language.updated", subjectLanguage, l.ID,
			map[string]any{"name": l.Name, "is_enabled": l.IsEnabled}, map[string]any{"name": name, "is_enabled": enabled})
	})
	if err != nil {
		return nil, err
	}
	if changed {
		h.translationsChanged(ctx)
	}
	return superadminapi.SaUpdateLanguage200JSONResponse(msg(h.d.T(ctx, "Язык обновлён.", nil))), nil
}

// SaDeleteLanguage — destroy(): встроенный нельзя; клубы языка переходят на язык по умолчанию.
func (h *handlers) SaDeleteLanguage(ctx context.Context, req superadminapi.SaDeleteLanguageRequestObject) (superadminapi.SaDeleteLanguageResponseObject, error) {
	err := h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLockLanguage(ctx, req.LanguageId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if l.IsSystem {
			return httpx.ValidationMsg("language", "Встроенные языки удалить нельзя, их можно только выключить.", nil)
		}
		clubs, err := q.SuperadminResetClubLocale(ctx, &l.Code)
		if err != nil {
			return err
		}
		if err := q.SuperadminDeleteLanguage(ctx, l.ID); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "language.deleted", Key: "Удалён язык: :name (:code)",
			Params: map[string]string{"name": l.Name, "code": l.Code}, SubjectType: subjectLanguage, SubjectID: l.ID,
			Details: map[string]any{"clubs_switched_to_default": clubs}}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "language.deleted", subjectLanguage, l.ID, map[string]any{"code": l.Code, "name": l.Name}, nil)
	})
	if err != nil {
		return nil, err
	}
	h.translationsChanged(ctx)
	return superadminapi.SaDeleteLanguage200JSONResponse(msg(h.d.T(ctx, "Язык удалён.", nil))), nil
}

// SaListTranslations — translations(): весь каталог, фильтр по тексту и «непереведённые», по 50 (в памяти, как
// LengthAwarePaginator старого кода: каталог — несколько тысяч строк).
func (h *handlers) SaListTranslations(ctx context.Context, req superadminapi.SaListTranslationsRequestObject) (superadminapi.SaListTranslationsResponseObject, error) {
	search := ""
	if req.Params.Search != nil {
		if utf8.RuneCountInString(*req.Params.Search) > 120 {
			return nil, httpx.RuleErrorWith("search", "max_string", "120")
		}
		search = strings.ToLower(*req.Params.Search)
	}
	missing := req.Params.Missing != nil && *req.Params.Missing
	pg := page(req.Params.Page)
	out := superadminapi.TranslationPage{Items: []superadminapi.TranslationRow{}, Page: pg, PerPage: translationsPage}
	err := h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLanguage(ctx, req.LanguageId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		out.Language.Id, out.Language.Code, out.Language.Name = l.ID, l.Code, l.Name
		values, err := q.SuperadminLanguageStrings(ctx, l.ID)
		if err != nil {
			return err
		}
		byHash := make(map[string]string, len(values))
		for _, v := range values {
			byHash[v.KeyHash] = v.Value
		}
		out.Translated = int64(len(values))
		src, order, err := sources(ctx, q)
		if err != nil {
			return err
		}
		out.SourceTotal = int64(len(src))
		var rows []superadminapi.TranslationRow
		for _, hash := range order {
			s := src[hash]
			label := s.key
			if s.group != "*" {
				label = s.group + "." + s.key
			}
			row := superadminapi.TranslationRow{Hash: hash, Label: label, Source: s.text, Value: byHash[hash]}
			if search != "" && !strings.Contains(strings.ToLower(row.Source+" "+row.Value+" "+row.Label), search) {
				continue
			}
			if missing && row.Value != "" {
				continue
			}
			rows = append(rows, row)
		}
		out.Total = int64(len(rows))
		from := (pg - 1) * translationsPage
		if from < len(rows) {
			out.Items = rows[from:min(len(rows), from+translationsPage)]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListTranslations200JSONResponse(out), nil
}

// apply — TranslationTransfer::apply(): вставить/изменить непустые, удалить пустые (deleteEmpty), неизвестные
// хэши — пропустить. Возвращает число изменённых строк. Удаление сдвигает updated_at языка (версия, PD-12).
func apply(ctx context.Context, q *store.Queries, languageID int64, values map[string]string, deleteEmpty bool, now *time.Time) (int, error) {
	src, _, err := sources(ctx, q)
	if err != nil {
		return 0, err
	}
	existing, err := q.SuperadminLanguageStrings(ctx, languageID)
	if err != nil {
		return 0, err
	}
	cur := make(map[string]store.SuperadminLanguageStringsRow, len(existing))
	for _, e := range existing {
		cur[e.KeyHash] = e
	}
	hashes := make([]string, 0, len(values))
	for hash := range values {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	var deletes []int64
	changed := 0
	for _, hash := range hashes {
		s, ok := src[hash]
		if !ok {
			continue
		}
		v := strings.TrimSpace(values[hash])
		row, has := cur[hash]
		switch {
		case v == "":
			if deleteEmpty && has {
				deletes = append(deletes, row.ID)
			}
		case !has || row.Value != v:
			if err := q.SuperadminUpsertTranslation(ctx, store.SuperadminUpsertTranslationParams{LanguageID: languageID, Grp: s.group,
				Key: s.key, KeyHash: hash, Value: v, Now: now}); err != nil {
				return 0, err
			}
			changed++
		}
	}
	if len(deletes) > 0 {
		if err := q.SuperadminDeleteTranslations(ctx, store.SuperadminDeleteTranslationsParams{LanguageID: languageID, Ids: deletes}); err != nil {
			return 0, err
		}
		if err := q.SuperadminTouchLanguage(ctx, store.SuperadminTouchLanguageParams{Now: now, ID: languageID}); err != nil {
			return 0, err
		}
	}
	return changed + len(deletes), nil
}

// SaSaveTranslations — saveTranslations(): страница переводов (до 200 полей), пустое — удалить перевод.
func (h *handlers) SaSaveTranslations(ctx context.Context, req superadminapi.SaSaveTranslationsRequestObject) (superadminapi.SaSaveTranslationsResponseObject, error) {
	if req.Body == nil || len(req.Body.Translations) == 0 {
		return nil, httpx.RuleError("translations", "required")
	}
	if len(req.Body.Translations) > maxSavePerPage {
		return nil, httpx.RuleErrorWith("translations", "max", strconv.Itoa(maxSavePerPage))
	}
	values := make(map[string]string, len(req.Body.Translations))
	var fe httpx.FormErrors
	for hash, v := range req.Body.Translations {
		if v == nil {
			values[hash] = ""
			continue
		}
		if utf8.RuneCountInString(*v) > maxTranslation {
			fe.Add("translations."+hash, httpx.RuleMessage("translations."+hash, "max_string", strconv.Itoa(maxTranslation)))
		}
		values[hash] = *v
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	changed := 0
	err := h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLockLanguage(ctx, req.LanguageId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if changed, err = apply(ctx, q, l.ID, values, true, &now); err != nil || changed == 0 {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "translations.updated", Key: "Изменены переводы: :name (:count)",
			Params: map[string]string{"name": l.Name, "count": strconv.Itoa(changed)}, SubjectType: subjectLanguage, SubjectID: l.ID,
			Details: map[string]any{"changed": changed}}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "translations.updated", subjectLanguage, l.ID, nil, map[string]any{"changed": changed})
	})
	if err != nil {
		return nil, err
	}
	if changed > 0 {
		h.translationsChanged(ctx)
	}
	return superadminapi.SaSaveTranslations200JSONResponse(msg(h.d.T(ctx,
		"Сохранён :count перевод.|Сохранено :count перевода.|Сохранено :count переводов.", map[string]string{"count": strconv.Itoa(changed)}))), nil
}

// translationsChanged — после правки: каталог процесса увидит новую версию на следующем запросе (по SELECT версии),
// агентам на связи — TranslationsChanged с новой версией (TranslationCatalog::flush(), PD-12).
func (h *handlers) translationsChanged(ctx context.Context) {
	if h.d.Superadmin.BroadcastOnline == nil {
		return
	}
	var version int64
	if err := h.tx(ctx, func(q *store.Queries) error {
		v, err := q.I18nVersion(ctx)
		version = v.ChangedMs
		return err
	}); err != nil {
		h.d.Log().WarnContext(ctx, "translations changed: version", "err", err)
		return
	}
	h.d.Superadmin.BroadcastOnline(ctx, &agentv1.Command{Kind: &agentv1.Command_TranslationsChanged{
		TranslationsChanged: &agentv1.TranslationsChanged{Version: version}}})
}

// ---------------------------------------------------------------------------
// Экспорт и импорт CSV (TranslationTransfer)
// ---------------------------------------------------------------------------

// csvResponse — ответ CSV с заголовками старого кода (text/csv; charset=UTF-8, no-store, имя файла).
type csvResponse struct {
	name string
	body []byte
}

func (r csvResponse) VisitSaExportTranslationsResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/csv; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+r.name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(r.body)))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(r.body)
	return err
}

// putCSV — fputcsv($out, $row, ';', '"', ”): поле в кавычках, если в нём ; " пробел, таб, \r или \n.
func putCSV(b *bytes.Buffer, row []string) {
	for i, f := range row {
		if i > 0 {
			b.WriteByte(';')
		}
		if strings.ContainsAny(f, ";\" \t\r\n") {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(f, `"`, `""`))
			b.WriteByte('"')
		} else {
			b.WriteString(f)
		}
	}
	b.WriteByte('\n')
}

// protectCell — ячейки, которые таблицы считают формулой (= + - @ таб \r), получают апостроф.
func protectCell(v string) string {
	if formulaRe.MatchString(v) {
		return "'" + v
	}
	return v
}

func unprotectCell(v string) string {
	if protectedRe.MatchString(v) {
		return v[1:]
	}
	return v
}

// SaExportTranslations — export(): id;group;ru;en;<код>, только непереведённые — по флагу.
func (h *handlers) SaExportTranslations(ctx context.Context, req superadminapi.SaExportTranslationsRequestObject) (superadminapi.SaExportTranslationsResponseObject, error) {
	missing := req.Params.Missing != nil && *req.Params.Missing
	var buf bytes.Buffer
	var name string
	err := h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLanguageByCode(ctx, req.Params.Language)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.RuleError("language", "exists")
		}
		if err != nil {
			return err
		}
		values, err := langValues(ctx, q, l.ID)
		if err != nil {
			return err
		}
		english := map[string]string{}
		if l.Code != i18n.FallbackLocale {
			if en, err := q.SuperadminLanguageByCode(ctx, i18n.FallbackLocale); err == nil {
				if english, err = langValues(ctx, q, en.ID); err != nil {
					return err
				}
			}
		}
		src, order, err := sources(ctx, q)
		if err != nil {
			return err
		}
		buf.WriteString("\xEF\xBB\xBF") // BOM: Excel открывает файл как UTF-8
		putCSV(&buf, []string{"id", "group", i18n.SourceLocale, i18n.FallbackLocale, l.Code})
		for _, hash := range order {
			v := values[hash]
			if missing && v != "" {
				continue
			}
			s := src[hash]
			putCSV(&buf, []string{protectCell(hash), protectCell(s.group), protectCell(s.text), protectCell(english[hash]), protectCell(v)})
		}
		suffix := ""
		if missing {
			suffix = "-missing"
		}
		name = "zafiro-translations-" + l.Code + suffix + "-" + h.d.NowFunc().UTC().Format("2006-01-02") + ".csv"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return csvResponse{name: name, body: buf.Bytes()}, nil
}

func langValues(ctx context.Context, q *store.Queries, id int64) (map[string]string, error) {
	rows, err := q.SuperadminLanguageStrings(ctx, id)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.KeyHash] = r.Value
	}
	return m, nil
}

// placeholdersMatch — подстановки :name перевода совпадают с оригиналом (без учёта регистра и порядка).
func placeholdersMatch(src, value string) bool {
	names := func(s string) []string {
		found := placeholderRe.FindAllString(s, -1)
		for i := range found {
			found[i] = strings.ToLower(found[i])
		}
		slices.Sort(found)
		return slices.Compact(found)
	}
	return slices.Equal(names(src), names(value))
}

// parseCSV — TranslationTransfer::parse(): BOM, UTF-8, разделитель — чаще всего встречающийся в первой строке
// из ; , таб (при равенстве — в этом порядке), не больше 10 000 строк.
func parseCSV(data []byte) ([][]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) {
		return nil, httpx.ValidationMsg("file", "Файл должен быть в кодировке UTF-8. В Excel выберите «CSV UTF-8».", nil)
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	delim, best := ';', -1
	for _, d := range []rune{';', ',', '\t'} {
		if n := bytes.Count(first, []byte(string(d))); n > best {
			delim, best = d, n
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord = delim, true, -1
	var rows [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				continue
			}
			return nil, err
		}
		if len(rows) > maxImportRows {
			return nil, httpx.ValidationMsg("file", "В файле слишком много строк.", nil)
		}
		rows = append(rows, rec)
	}
	return rows, nil
}

// readImport — multipart: language, file (≤ 2 МБ, csv/txt).
func readImport(mr *multipart.Reader) (string, []byte, bool, error) {
	var lang string
	var file []byte
	hasFile := false
	if mr == nil {
		return "", nil, false, nil
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, false, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		switch part.FormName() {
		case "language":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			lang = strings.TrimSpace(string(b))
		case "file":
			if part.FileName() == "" {
				continue
			}
			hasFile = true
			file, err = io.ReadAll(io.LimitReader(part, maxImportBytes+1))
			if err != nil {
				return "", nil, false, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
			}
		}
	}
	return lang, file, hasFile, nil
}

// SaImportTranslations — import(): меняются только заполненные ячейки; строки с другими подстановками — пропуск.
func (h *handlers) SaImportTranslations(ctx context.Context, req superadminapi.SaImportTranslationsRequestObject) (superadminapi.SaImportTranslationsResponseObject, error) {
	code, data, hasFile, err := readImport(req.Body)
	if err != nil {
		return nil, err
	}
	var fe httpx.FormErrors
	if code == "" {
		fe.Add("language", httpx.RuleMessage("language", "required", ""))
	}
	switch {
	case !hasFile:
		fe.Add("file", httpx.RuleMessage("file", "required", ""))
	case len(data) > maxImportBytes:
		fe.Add("file", httpx.RuleMessage("file", "max", "2048"))
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var stats struct{ changed, unchanged, empty, unknown int }
	var invalid []string
	var langName string
	err = h.tx(ctx, func(q *store.Queries) error {
		l, err := q.SuperadminLanguageByCode(ctx, code)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.RuleError("language", "exists")
		}
		if err != nil {
			return err
		}
		langName = l.Name
		rows, err := parseCSV(data)
		if err != nil {
			return err
		}
		var header []string
		if len(rows) > 0 {
			for _, c := range rows[0] {
				header = append(header, strings.ToLower(strings.TrimSpace(c)))
			}
			rows = rows[1:]
		}
		if len(header) < 5 || header[0] != "id" {
			return httpx.ValidationMsg("file", "Это не файл переводов Zafiro: нужна первая строка «id;group;ru;en;код языка».", nil)
		}
		if header[4] != l.Code {
			return httpx.ValidationMsg("file", "Файл выгружен для языка «:file», а импорт идёт в «:language».",
				map[string]string{"file": header[4], "language": l.Code})
		}
		src, _, err := sources(ctx, q)
		if err != nil {
			return err
		}
		submitted := map[string]string{}
		for _, row := range rows {
			cell := func(i int) string {
				if i < len(row) {
					return row[i]
				}
				return ""
			}
			hash := strings.TrimSpace(cell(0))
			value := strings.TrimSpace(unprotectCell(cell(4)))
			if hash == "" && value == "" {
				continue
			}
			s, ok := src[hash]
			switch {
			case !ok:
				stats.unknown++
			case value == "":
				stats.empty++
			case !placeholdersMatch(s.text, value):
				invalid = append(invalid, s.text)
			default:
				submitted[hash] = value
			}
		}
		if stats.changed, err = apply(ctx, q, l.ID, submitted, false, &now); err != nil {
			return err
		}
		stats.unchanged = len(submitted) - stats.changed
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "translations.imported", Key: "Импорт переводов: :name (:count)",
			Params: map[string]string{"name": l.Name, "count": strconv.Itoa(stats.changed)}, SubjectType: subjectLanguage, SubjectID: l.ID,
			Details: map[string]any{"changed": stats.changed, "unchanged": stats.unchanged, "empty": stats.empty, "unknown": stats.unknown,
				"invalid_placeholders": len(invalid)}}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "translations.imported", subjectLanguage, l.ID, nil, map[string]any{"changed": stats.changed})
	})
	if err != nil {
		return nil, err
	}
	if stats.changed > 0 {
		h.translationsChanged(ctx)
	}
	out := superadminapi.ImportResult{InvalidCount: len(invalid), Invalid: invalid[:min(len(invalid), 20)],
		Message: h.d.T(ctx, "Импорт :name: обновлено :changed, без изменений :unchanged, пустых :empty, неизвестных строк :unknown.",
			map[string]string{"name": langName, "changed": strconv.Itoa(stats.changed), "unchanged": strconv.Itoa(stats.unchanged),
				"empty": strconv.Itoa(stats.empty), "unknown": strconv.Itoa(stats.unknown)})}
	if out.Invalid == nil {
		out.Invalid = []string{}
	}
	return superadminapi.SaImportTranslations200JSONResponse(out), nil
}
