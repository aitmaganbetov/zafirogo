package migrate

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Severity — важность находки.
type Severity string

const (
	// Blocker — перенос невозможен, пока данные не исправлены (apply отказывается).
	Blocker Severity = "blocker"
	// Warning — переносится с нормализацией или пометкой (решения Q-004, Q-027, Q-030 …).
	Warning Severity = "warning"
	// Info — к сведению (пропущенные таблицы, параметры источника).
	Info Severity = "info"
)

// Коды находок (стабильные: по ним фильтруют отчёт и проверяют тесты).
const (
	CodeEnumOutsideCheck  = "Q-026"    // значение вне CHECK Postgres
	CodeCaseConflict      = "Q-025"    // конфликт без учёта регистра
	CodeNullCreatedAt     = "Q-030"    // created_at = NULL
	CodeNegativeBalance   = "Q-004"    // отрицательный баланс → входящий остаток с пометкой
	CodeOldRole           = "Q-027"    // старая роль → client
	CodeSettingsLegacy    = "Q-022"    // клуб legacy без settings
	CodeSettingsFormat    = "Q-023"    // значение/ключ настроек вне формата
	CodeNullNotNull       = "not_null" // NULL в колонке NOT NULL Postgres
	CodeBadValue          = "value"    // значение не преобразуется (дата, сумма, JSON)
	CodeUnmappedColumn    = "column"   // колонка MySQL без пары в Postgres
	CodeDanglingUser      = "user_ref" // ссылка на несуществующий аккаунт
	CodeUserConflict      = "user"     // аккаунт в Postgres с тем же телефоном, но другим id и т. п.
	CodeUserKept          = "user_kept"
	CodePlanConflict      = "saas_plan"
	CodeAlreadyMigrated   = "migrated"
	CodeSkippedTable      = "skipped"
	CodeRetention         = "retention"
	CodeSource            = "source"
	CodeMinutesNotMember  = "minutes"
	CodeVerifyMismatch    = "verify"
	CodeExtension         = "extension"
	CodeFreeze            = "freeze" // заморозка клуба (подписка приостановлена в старом Laravel)
	maxSamples            = 5
	maxHistoryLines       = 30
	maxDurationLines      = 12
	maxFindingValueLength = 80
)

// Finding — находка плана/переноса/сверки, агрегированная по (код, таблица, колонка, значение).
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Table    string   `json:"table,omitempty"`
	Column   string   `json:"column,omitempty"`
	Value    string   `json:"value,omitempty"`
	Message  string   `json:"message"`
	Count    int64    `json:"count"`
	// Samples — id строк (до maxSamples).
	Samples []string `json:"samples,omitempty"`
}

// TableStat — строки таблицы в источнике и (после apply/verify) в Postgres.
type TableStat struct {
	Table  string `json:"table"`
	Source int64  `json:"source"`
	// Written — вставлено в Postgres (apply); у платформенных таблиц меньше Source, если строки уже были.
	Written int64 `json:"written,omitempty"`
	// Skipped — строк источника, отброшенных правилом таблицы (table.Transform; например, истёкшие токены).
	Skipped int64 `json:"skipped,omitempty"`
	// Target — строк клуба в Postgres (verify).
	Target int64 `json:"target,omitempty"`
	// Converted — что преобразовано (колонка → вид), для отчёта плана.
	Converted map[string]string `json:"converted,omitempty"`
}

// Report — отчёт plan/apply/verify одного клуба.
type Report struct {
	Mode     string `json:"mode"`
	ClubID   int64  `json:"club_id"`
	ClubSlug string `json:"club_slug"`
	ClubName string `json:"club_name"`
	// Tables — по порядку переноса.
	Tables   []*TableStat      `json:"tables"`
	Findings []*Finding        `json:"findings"`
	Totals   map[string]string `json:"totals,omitempty"`
	// Durations — время шагов (мс): таблицы, входящие остатки, расширения, сверка (замер объёмов, README «Объёмы»).
	Durations map[string]int64 `json:"durations_ms,omitempty"`
	// BalanceHistory — Q-005: клиенты, у которых остаток не равен сумме движений истории старой базы.
	BalanceHistory []BalanceHistoryDiff `json:"balance_history,omitempty"`

	index map[string]*Finding
	stats map[string]*TableStat
}

func newReport(mode string) *Report {
	return &Report{Mode: mode, index: map[string]*Finding{}, stats: map[string]*TableStat{}, Totals: map[string]string{},
		Durations: map[string]int64{}}
}

// timed — время шага в отчёт (Durations; повторный шаг с тем же именем складывается).
func (r *Report) timed(step string, began time.Time) {
	r.Durations[step] += time.Since(began).Milliseconds()
}

func (r *Report) table(name string) *TableStat {
	if s, ok := r.stats[name]; ok {
		return s
	}
	s := &TableStat{Table: name}
	r.stats[name] = s
	r.Tables = append(r.Tables, s)
	return s
}

// add добавляет находку (или увеличивает счётчик такой же); sample — id строки (может быть пустым).
func (r *Report) add(sev Severity, code, table, column, value, sample, message string) {
	if len(value) > maxFindingValueLength {
		value = value[:maxFindingValueLength] + "…"
	}
	key := strings.Join([]string{string(sev), code, table, column, value}, "\x00")
	f, ok := r.index[key]
	if !ok {
		f = &Finding{Severity: sev, Code: code, Table: table, Column: column, Value: value, Message: message}
		r.index[key] = f
		r.Findings = append(r.Findings, f)
	}
	f.Count++
	if sample != "" && len(f.Samples) < maxSamples {
		f.Samples = append(f.Samples, sample)
	}
}

// Count — число находок данной важности (сумма счётчиков).
func (r *Report) Count(sev Severity) int64 {
	var n int64
	for _, f := range r.Findings {
		if f.Severity == sev {
			n += f.Count
		}
	}
	return n
}

// Has сообщает, есть ли находка с кодом (и важностью, если sev не пустая).
func (r *Report) Has(sev Severity, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code && (sev == "" || f.Severity == sev) {
			return true
		}
	}
	return false
}

// Find возвращает находки с кодом.
func (r *Report) Find(code string) []*Finding {
	var out []*Finding
	for _, f := range r.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

// Stat возвращает статистику таблицы (nil — таблицы нет в отчёте).
func (r *Report) Stat(table string) *TableStat { return r.stats[table] }

// WriteJSON пишет отчёт в JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText пишет отчёт для человека.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "migrate-from-mysql %s: клуб #%d %q (%s)\n\n", r.Mode, r.ClubID, r.ClubName, r.ClubSlug)
	b.WriteString("Таблицы:\n")
	for _, t := range r.Tables {
		if r.Mode == "purge" {
			fmt.Fprintf(&b, "  %-24s удалено %8d\n", t.Table, t.Target)
			continue
		}
		fmt.Fprintf(&b, "  %-24s MySQL %8d", t.Table, t.Source)
		if t.Written > 0 || r.Mode == "apply" {
			fmt.Fprintf(&b, "  записано %8d", t.Written)
		}
		if r.Mode == "verify" {
			fmt.Fprintf(&b, "  Postgres %8d", t.Target)
		}
		if len(t.Converted) > 0 {
			cols := make([]string, 0, len(t.Converted))
			for c, k := range t.Converted {
				cols = append(cols, c+"→"+k)
			}
			sort.Strings(cols)
			b.WriteString("  [" + strings.Join(cols, ", ") + "]")
		}
		b.WriteByte('\n')
	}
	if len(r.Totals) > 0 {
		b.WriteString("\nИтоги:\n")
		keys := make([]string, 0, len(r.Totals))
		for k := range r.Totals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-40s %s\n", k, r.Totals[k])
		}
	}
	if len(r.Durations) > 0 {
		b.WriteString("\nВремя шагов:\n")
		steps := make([]string, 0, len(r.Durations))
		for k := range r.Durations {
			steps = append(steps, k)
		}
		sort.Slice(steps, func(i, j int) bool { return r.Durations[steps[i]] > r.Durations[steps[j]] })
		for i, k := range steps {
			if i == maxDurationLines {
				break
			}
			fmt.Fprintf(&b, "  %-40s %8d мс\n", k, r.Durations[k])
		}
	}
	if len(r.BalanceHistory) > 0 {
		fmt.Fprintf(&b, "\nQ-005: остаток ≠ сумма движений истории (%d клиентов; тенге: остаток / история / разница):\n", len(r.BalanceHistory))
		for i, d := range r.BalanceHistory {
			if i == maxHistoryLines {
				fmt.Fprintf(&b, "  … ещё %d (полный список — отчёт --json, balance_history)\n", len(r.BalanceHistory)-i)
				break
			}
			fmt.Fprintf(&b, "  клиент %-8d основной %s / %s / %s; бонусный %s / %s / %s", d.UserID,
				tiynString(d.Main), tiynString(d.HistoryMain), tiynString(d.DiffMain),
				tiynString(d.Bonus), tiynString(d.HistoryBonus), tiynString(d.DiffBonus))
			if d.NoBalanceRow {
				b.WriteString(" (нет строки баланса)")
			}
			b.WriteByte('\n')
		}
	}
	for _, sev := range []Severity{Blocker, Warning, Info} {
		var list []*Finding
		for _, f := range r.Findings {
			if f.Severity == sev {
				list = append(list, f)
			}
		}
		if len(list) == 0 {
			continue
		}
		title := map[Severity]string{Blocker: "БЛОКЕРЫ (перенос невозможен)", Warning: "Предупреждения", Info: "К сведению"}[sev]
		fmt.Fprintf(&b, "\n%s:\n", title)
		for _, f := range list {
			where := f.Table
			if f.Column != "" {
				where += "." + f.Column
			}
			fmt.Fprintf(&b, "  [%s] %s", f.Code, f.Message)
			if where != "" {
				b.WriteString(" — " + where)
			}
			if f.Value != "" {
				b.WriteString(" = " + strconv.Quote(f.Value))
			}
			fmt.Fprintf(&b, " (%d)", f.Count)
			if len(f.Samples) > 0 {
				b.WriteString(" id: " + strings.Join(f.Samples, ", "))
			}
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "\nИтого: блокеров %d, предупреждений %d.\n", r.Count(Blocker), r.Count(Warning))
	_, err := io.WriteString(w, b.String())
	return err
}
