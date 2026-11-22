package superadmin

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/monitoring"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Laravel: app/Http/Controllers/Superadmin/DashboardController.php, config/monitoring.php.
const (
	clubLoadRows         = 50 // CLUB_LOAD_ROWS
	clubLoadLines        = 8  // CLUB_LOAD_LINES
	clubLoadDefaultLines = 5  // CLUB_LOAD_DEFAULT_LINES
	schedulerSilent      = 5  // monitoring.scheduler_silent_minutes
	diskPercentAlert     = 85 // monitoring.disk_percent
	queuePendingAlert    = 100
)

// periods — period => [длина в минутах, бакет в минутах].
var periods = map[string][2]int64{"24h": {1440, 10}, "7d": {10080, 60}}

// channelLabels — DashboardController::channelLabels() (ключи перевода).
var channelLabels = map[string]string{
	monitoring.ChannelPanel: "Панель", monitoring.ChannelAgent: "Агенты ПК", monitoring.ChannelAPI: "API v1",
	monitoring.ChannelGuest: "Приложение гостя",
}

var digitsRe = regexp.MustCompile(`^[0-9]+$`)

func (h *handlers) SaDashboard(ctx context.Context, req superadminapi.SaDashboardRequestObject) (superadminapi.SaDashboardResponseObject, error) {
	now := h.d.NowFunc()
	period := "24h"
	if req.Params.Period != nil {
		if _, ok := periods[string(*req.Params.Period)]; ok {
			period = string(*req.Params.Period)
		}
	}
	length, bucket := periods[period][0], periods[period][1]
	nowMin := now.Unix() / 60
	w := window{length: length, bucket: bucket, from: nowMin - length}
	for b := w.from - w.from%bucket; b <= nowMin-nowMin%bucket; b += bucket {
		w.buckets = append(w.buckets, b)
	}

	out := superadminapi.Dashboard{Period: period, PeriodMinutes: length, SchedulerSilentMinutes: schedulerSilent,
		Today: now.UTC().Format("2006-01-02")}
	probe := monitoring.Probe{}
	live := probe.Snapshot(ctx, h.d.PlatformPool)
	var tables []monitoring.Table
	if h.d.PlatformPool != nil {
		tables = monitoring.BiggestTables(ctx, h.d.PlatformPool)
	}
	online := h.online(ctx)

	err := h.tx(ctx, func(q *store.Queries) error {
		serverDuration, err := serverCharts(ctx, q, &out, w, live)
		if err != nil {
			return err
		}
		if err := clubLoad(ctx, q, &out, w, req.Params.Clubs, serverDuration); err != nil {
			return err
		}

		// Сверка балансов с журналом операций (аудит AC-3): открытые расхождения и давность последней успешной.
		rec, err := q.SuperadminReconcileState(ctx)
		if err != nil {
			return err
		}
		out.LedgerReconcile = ledgerReconcileOf(rec, h.d.Superadmin.LedgerReconcileInterval, time.Now(), h.d.Locale(ctx))

		// Планировщик: последний снимок сервера.
		if latest, err := q.SuperadminLatestServerMetric(ctx); err == nil {
			ago := nowMin - latest
			out.SchedulerMinutesAgo = &ago
		}

		if err := channelsOf(ctx, q, &out, nowMin); err != nil {
			return err
		}
		if err := agentsOf(ctx, q, &out, online); err != nil {
			return err
		}

		// Последние 10 записей журнала платформы.
		logs, err := q.SuperadminPlatformLogs(ctx, store.SuperadminPlatformLogsParams{PageSize: 10})
		if err != nil {
			return err
		}
		out.RecentLogs = h.rows(ctx, platformRows(logs), true)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out.Live = liveOf(live)
	out.Tables = []superadminapi.TableSize{}
	for _, t := range tables {
		out.Tables = append(out.Tables, superadminapi.TableSize{Name: t.Name, Megabytes: phpNum(t.Megabytes, 1), Rows: t.Rows})
	}
	// Журнал ошибок (кольцевой буфер slog) и проверка сертификатов — Этап 4 (Q-060, PD-5): пока пусто.
	out.LogErrors = []superadminapi.LogError{}
	out.CertificateHosts = []superadminapi.CertificateHost{}
	for _, host := range h.d.Superadmin.SSLHosts {
		out.CertificateHosts = append(out.CertificateHosts, superadminapi.CertificateHost{Host: host})
	}
	out.Backup = backupOf(h.d.Superadmin.BackupPath, now, h.d.Locale(ctx))
	return superadminapi.SaDashboard200JSONResponse(out), nil
}

// window — период графиков экрана: длина и бакет в минутах, первая минута и бакеты оси.
type window struct {
	length, bucket, from int64
	buckets              []int64
}

// serverCharts — графики сервера: нагрузка CPU, память, запросы и время ответа по каналам, доля ошибок 5xx.
// serverDuration — суммарное время ответов за период (доля клуба в таблице нагрузки клубов).
func serverCharts(ctx context.Context, q *store.Queries, out *superadminapi.Dashboard, w window, live monitoring.Snapshot) (serverDuration int64, err error) {
	bucket := w.bucket
	server, err := q.SuperadminServerBuckets(ctx, store.SuperadminServerBucketsParams{Bucket: bucket, FromMinute: w.from})
	if err != nil {
		return 0, err
	}
	maxLoad, err := q.SuperadminServerMaxLoad(ctx, w.from)
	if err != nil {
		return 0, err
	}
	reqs, err := q.SuperadminRequestBuckets(ctx, store.SuperadminRequestBucketsParams{Bucket: bucket, FromMinute: w.from})
	if err != nil {
		return 0, err
	}
	load := series{label: "Нагрузка CPU", translate: true, points: map[int64]float64{}}
	mem := series{label: "Память, %", translate: true, points: map[int64]float64{}}
	for _, r := range server {
		// AVG без значений (снимок без /proc) в SQL — 0: строки бакета есть, только когда планировщик работал.
		load.points[r.Bucket] = roundF(r.Load1, 2)
		mem.points[r.Bucket] = roundF(r.Memory, 1)
	}
	cpu := 1.0
	if live.CPUCount != nil {
		cpu = float64(*live.CPUCount)
	}
	loadMax := math.Max(cpu, maxLoad) * 1.1
	hundred := 100.0
	perChannel := func(value func(r store.SuperadminRequestBucketsRow) (float64, bool)) []series {
		var out []series
		for _, ch := range monitoring.Channels {
			s := series{label: channelLabels[ch], translate: true, points: map[int64]float64{}}
			for _, r := range reqs {
				if r.Channel == ch {
					if v, ok := value(r); ok {
						s.points[r.Bucket] = v
					}
				}
			}
			out = append(out, s)
		}
		return out
	}
	errSeries := series{label: "Ошибки 5xx, %", translate: true, points: map[int64]float64{}}
	type tot struct{ req, errs int64 }
	byBucket := map[int64]tot{}
	for _, r := range reqs {
		t := byBucket[r.Bucket]
		t.req += r.Requests
		t.errs += r.Errors
		byBucket[r.Bucket] = t
		serverDuration += r.DurationMs
	}
	for b, t := range byBucket {
		v := 0.0
		if t.req > 0 {
			v = roundF(float64(t.errs)*100/float64(t.req), 2)
		}
		errSeries.points[b] = v
	}
	out.Charts.Load = toChart(makeChart(w.buckets, []series{load}, &loadMax))
	out.Charts.Memory = toChart(makeChart(w.buckets, []series{mem}, &hundred))
	out.Charts.Requests = toChart(makeChart(w.buckets, perChannel(func(r store.SuperadminRequestBucketsRow) (float64, bool) {
		return roundF(float64(r.Requests)/float64(bucket), 1), true
	}), nil))
	out.Charts.Duration = toChart(makeChart(w.buckets, perChannel(func(r store.SuperadminRequestBucketsRow) (float64, bool) {
		if r.Requests <= 0 {
			return 0, false
		}
		return float64(intRound(float64(r.DurationMs) / float64(r.Requests))), true
	}), nil))
	out.Charts.Errors = toChart(makeChart(w.buckets, []series{errSeries}, nil))
	return serverDuration, nil
}

// clubLoad — нагрузка клубов: таблица самых нагруженных за период и график выбранных (параметр clubs; без него —
// первые clubLoadDefaultLines из таблицы, не больше clubLoadLines линий).
func clubLoad(ctx context.Context, q *store.Queries, out *superadminapi.Dashboard, w window, clubs *[]string, serverDuration int64) error {
	rows, err := q.SuperadminClubLoad(ctx, store.SuperadminClubLoadParams{FromMinute: w.from, RowLimit: clubLoadRows})
	if err != nil {
		return err
	}
	var asked []int64
	seen := map[int64]bool{}
	if clubs != nil {
		for _, s := range *clubs {
			if !digitsRe.MatchString(s) {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil || seen[id] {
				continue
			}
			seen[id] = true
			asked = append(asked, id)
		}
	}
	selected := asked
	if len(selected) == 0 {
		for _, r := range rows {
			if len(selected) == clubLoadDefaultLines {
				break
			}
			selected = append(selected, r.ClubID)
		}
	}
	if len(selected) > clubLoadLines {
		selected = selected[:clubLoadLines]
	}
	ids := append([]int64{}, selected...)
	for _, r := range rows {
		ids = append(ids, r.ClubID)
	}
	names := map[int64]string{}
	nameRows, err := q.SuperadminClubNames(ctx, ids)
	if err != nil {
		return err
	}
	for _, n := range nameRows {
		names[n.ID] = n.Name
	}
	var clubSeries []series
	if len(selected) > 0 {
		points, err := q.SuperadminClubLoadPoints(ctx, store.SuperadminClubLoadPointsParams{Bucket: w.bucket, FromMinute: w.from, ClubIds: selected})
		if err != nil {
			return err
		}
		used := map[string]bool{}
		for _, id := range selected {
			label, ok := names[id]
			if !ok {
				label = "#" + strconv.FormatInt(id, 10)
			}
			if used[label] { // два клуба с одним именем — две линии
				label += " #" + strconv.FormatInt(id, 10)
			}
			used[label] = true
			s := series{label: label, points: map[int64]float64{}}
			for _, p := range points {
				if p.ClubID == id {
					s.points[p.Bucket] = roundF(float64(p.Requests)/float64(w.bucket), 1)
				}
			}
			clubSeries = append(clubSeries, s)
		}
	}
	out.Charts.Clubs = toChart(makeChart(w.buckets, clubSeries, nil))
	out.SelectedClubs = selected
	if out.SelectedClubs == nil {
		out.SelectedClubs = []int64{}
	}
	out.ClubLoad = []superadminapi.ClubLoadRow{}
	for _, r := range rows {
		name, ok := names[r.ClubID]
		if !ok {
			name = "#" + strconv.FormatInt(r.ClubID, 10)
		}
		row := superadminapi.ClubLoadRow{ClubId: r.ClubID, Name: name,
			RequestsPerMinute: phpNum(float64(r.Requests)/float64(w.length), 1), Peak: r.Peak,
			Agent: numberFormat(r.Agent, " "), Api: numberFormat(r.Api, " "), Panel: numberFormat(r.Panel, " "),
			Guest: numberFormat(r.Guest, " "), HasErrors: r.Errors > 0}
		if r.Requests > 0 {
			ms := strconv.FormatInt(intRound(float64(r.DurationMs)/float64(r.Requests)), 10)
			pct := phpNum(float64(r.Errors)*100/float64(r.Requests), 1) + "%"
			row.ResponseMs, row.ErrorsPercent = &ms, &pct
		}
		if serverDuration > 0 {
			share := phpNum(float64(r.DurationMs)*100/float64(serverDuration), 1) + "%"
			row.ServerShare = &share
		}
		out.ClubLoad = append(out.ClubLoad, row)
	}
	return nil
}

// channelsOf — запросы по каналам за последние 15 минут.
func channelsOf(ctx context.Context, q *store.Queries, out *superadminapi.Dashboard, nowMin int64) error {
	chs, err := q.SuperadminRequestChannels(ctx, nowMin-15)
	if err != nil {
		return err
	}
	out.Channels = []superadminapi.ChannelRow{}
	for _, ch := range monitoring.Channels {
		row := superadminapi.ChannelRow{Channel: ch, Label: channelLabels[ch], RequestsPerMinute: "0"}
		for _, c := range chs {
			if c.Channel != ch {
				continue
			}
			row.RequestsPerMinute = phpNum(float64(c.Requests)/15, 1)
			if c.Requests > 0 {
				ms := strconv.FormatInt(intRound(float64(c.DurationMs)/float64(c.Requests)), 10)
				pct := phpNum(float64(c.Errors)*100/float64(c.Requests), 1) + "%"
				row.ResponseMs, row.ErrorsPercent, row.HasErrors = &ms, &pct, c.Errors > 0
			}
		}
		out.Channels = append(out.Channels, row)
	}
	return nil
}

// agentsOf — ПК (всего и в сети) и версии агента против последнего выпуска.
func agentsOf(ctx context.Context, q *store.Queries, out *superadminapi.Dashboard, online map[int64]int64) error {
	total, err := q.SuperadminComputerCount(ctx)
	if err != nil {
		return err
	}
	out.Computers.Total = total
	for _, n := range online {
		out.Computers.Online += n
	}
	rel, err := readRelease(ctx, q)
	if err != nil {
		return err
	}
	latestVersion := rel.latestVersion()
	if latestVersion != "" {
		out.LatestAgent = &latestVersion
	}
	out.AgentDownloadUrl = rel.downloadURL()
	if rel.raw != nil {
		out.AgentRelease = &superadminapi.AgentRelease{LatestVersion: rel.LatestVersion, ReleasedAt: rel.ReleasedAt,
			DownloadUrl: rel.DownloadURL, AutoUpdate: rel.AutoUpdate, InstallerUrl: rel.InstallerURL, InstallerSha256: rel.InstallerSHA256}
	}
	versions, err := q.SuperadminAgentVersions(ctx)
	if err != nil {
		return err
	}
	out.AgentVersions = []superadminapi.AgentVersion{}
	for _, v := range versions {
		out.AgentVersions = append(out.AgentVersions, superadminapi.AgentVersion{Version: v.AgentVersion, Count: v.Aggregate,
			Outdated: latestVersion != "" && v.AgentVersion != latestVersion})
	}
	return nil
}

// toChart — график для ответа API.
func toChart(c chart) superadminapi.Chart {
	out := superadminapi.Chart{Lines: []superadminapi.ChartLine{}, MaxLabel: c.MaxLabel, FromLabel: chartTime(c.From), ToLabel: chartTime(c.To)}
	for _, l := range c.Lines {
		out.Lines = append(out.Lines, superadminapi.ChartLine{Label: l.Label, Translate: l.Translate, Color: l.Color, Segments: l.Segments})
	}
	return out
}

// roundF — round($x, n) PHP как число.
func roundF(v float64, places int) float64 {
	f, _ := strconv.ParseFloat(phpNum(v, places), 64)
	return f
}

func liveOf(s monitoring.Snapshot) superadminapi.Live {
	l := superadminapi.Live{DiskAlertPercent: diskPercentAlert, QueuePending: s.QueuePending, QueuePendingAlert: queuePendingAlert,
		QueueFailed: s.QueueFailed, DatabaseMb: s.DatabaseMB}
	str := func(p *float64) *string {
		if p == nil {
			return nil
		}
		v := phpNum(*p, 2)
		return &v
	}
	l.Load1, l.Load5, l.Load15 = str(s.Load1), str(s.Load5), str(s.Load15)
	cpu := 1
	if s.CPUCount != nil {
		c := int(*s.CPUCount)
		l.CpuCount = &c
		cpu = max(1, c)
	}
	l.LoadAlert = s.Load5 != nil && *s.Load5 > float64(cpu)
	if s.MemoryTotalMB != nil && *s.MemoryTotalMB > 0 && s.MemoryUsedMB != nil {
		p := int(intRound(float64(*s.MemoryUsedMB) * 100 / float64(*s.MemoryTotalMB)))
		used, total := phpNum(float64(*s.MemoryUsedMB)/1024, 1), phpNum(float64(*s.MemoryTotalMB)/1024, 1)
		l.MemoryPercent, l.MemoryUsedGb, l.MemoryTotalGb = &p, &used, &total
	}
	if s.DiskTotalMB != nil && *s.DiskTotalMB > 0 && s.DiskUsedMB != nil {
		p := int(intRound(float64(*s.DiskUsedMB) * 100 / float64(*s.DiskTotalMB)))
		used := strconv.FormatInt(intRound(float64(*s.DiskUsedMB)/1024), 10)
		total := strconv.FormatInt(intRound(float64(*s.DiskTotalMB)/1024), 10)
		l.DiskPercent, l.DiskUsedGb, l.DiskTotalGb = &p, &used, &total
	}
	if s.UptimeSeconds != nil {
		d := *s.UptimeSeconds / 86400
		l.UptimeDays = &d
	}
	if s.QueueOldestSeconds != nil {
		m := *s.QueueOldestSeconds / 60
		l.QueueOldestMinutes = &m
	}
	return l
}

// ledgerReconcileStalePeriods — сколько периодов без успешной сверки считается остановкой (как алерт
// ZafiroLedgerReconcileStale: три часа при периоде в час).
const ledgerReconcileStalePeriods = 3

// ledgerReconcileOf — строка «Сверка балансов с журналом» экрана «Сервер» (новая, аудит AC-3). Время сверки пишет
// воркер по настоящим часам, поэтому и давность считается от них (now), а не от dev.fixed_now.
func ledgerReconcileOf(r store.SuperadminReconcileStateRow, interval time.Duration, now time.Time, locale string) superadminapi.LedgerReconcile {
	if interval <= 0 {
		interval = time.Hour
	}
	out := superadminapi.LedgerReconcile{OpenFindings: r.OpenFindings}
	if r.LastSuccessAt != nil {
		human := httpx.DiffForHumans(*r.LastSuccessAt, now, locale)
		out.AtHuman = &human
		out.Stale = now.Sub(*r.LastSuccessAt) > ledgerReconcileStalePeriods*interval
	}
	return out
}

// backupOf — DashboardController::lastBackup(): время самого нового файла каталога бэкапов.
func backupOf(path string, now time.Time, locale string) superadminapi.Backup {
	st, err := os.Stat(path)
	if path == "" || err != nil || !st.IsDir() {
		return superadminapi.Backup{Stale: true}
	}
	b := superadminapi.Backup{Configured: true, Stale: true}
	entries, _ := os.ReadDir(path)
	var newest time.Time
	for _, e := range entries {
		if fi, err := os.Stat(filepath.Join(path, e.Name())); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if !newest.IsZero() {
		human := httpx.DiffForHumans(newest, now, locale)
		b.AtHuman = &human
		b.Stale = newest.Before(now.AddDate(0, 0, -2))
	}
	return b
}

// sortedKeys — ключи карты по возрастанию (детерминированный вывод).
func sortedKeys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
