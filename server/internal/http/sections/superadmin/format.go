package superadmin

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// phpNum — вывод PHP float в шаблоне ({{ round($x, n) }}): round() PHP 8.4 и кратчайшая запись («1.5», «2»).
func phpNum(v float64, places int) string {
	r := money.PHPRound(v, places)
	if r == 0 {
		r = 0 // -0 → 0
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

// numberFormat — number_format($n, 0, $dec, $sep) для целых.
func numberFormat(n int64, sep string) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// intRound — (int) round($x) (PHP 8.4 round без знаков = math.Round).
func intRound(v float64) int64 { return int64(math.Round(v)) }

// ---------------------------------------------------------------------------
// LineChart (app/Support/LineChart.php): данные маленького SVG-графика.
// ---------------------------------------------------------------------------

const (
	chartWidth  = 600
	chartHeight = 160
)

var chartColors = []string{"#7c3aed", "#0ea5e9", "#f59e0b", "#10b981", "#ef4444", "#ec4899", "#14b8a6", "#64748b"}

// series — одна линия: подпись (translate — ключ перевода, иначе данные: имя клуба) и значения по бакетам.
type series struct {
	label     string
	translate bool
	points    map[int64]float64
}

type chartLine struct {
	Label     string
	Translate bool
	Color     string
	Segments  []string
}

type chart struct {
	Lines    []chartLine
	MaxLabel string
	From, To *int64
}

// makeChart — LineChart::make(): линия на серию, разрыв там, где у бакета нет значения; max по умолчанию —
// max(1, наибольшее × 1.1).
func makeChart(buckets []int64, ss []series, scale *float64) chart {
	var m float64
	if scale != nil {
		m = *scale
	} else {
		var top float64
		for _, s := range ss {
			for _, v := range s.points {
				top = math.Max(top, v)
			}
		}
		m = math.Max(1, top*1.1)
	}
	step := 0.0
	if len(buckets) > 1 {
		step = float64(chartWidth) / float64(len(buckets)-1)
	}
	out := chart{}
	for i, s := range ss {
		line := chartLine{Label: s.label, Translate: s.translate, Color: chartColors[i%len(chartColors)], Segments: []string{}}
		var cur []string
		for pos, b := range buckets {
			v, ok := s.points[b]
			if !ok {
				if len(cur) > 0 {
					line.Segments = append(line.Segments, strings.Join(cur, " "))
				}
				cur = nil
				continue
			}
			y := chartHeight - math.Min(1, v/m)*chartHeight
			cur = append(cur, phpNum(float64(pos)*step, 1)+","+phpNum(y, 1))
		}
		if len(cur) > 0 {
			line.Segments = append(line.Segments, strings.Join(cur, " "))
		}
		out.Lines = append(out.Lines, line)
	}
	// Шкала до :max: $chart['max'] >= 10 ? (int) round($chart['max']) : round($chart['max'], 1).
	if m >= 10 {
		out.MaxLabel = strconv.FormatInt(intRound(m), 10)
	} else {
		out.MaxLabel = phpNum(m, 1)
	}
	if len(buckets) > 0 {
		out.From, out.To = &buckets[0], &buckets[len(buckets)-1]
	}
	return out
}

// chartTime — Carbon::createFromTimestamp($minute * 60)->format('d.m H:i') (app.timezone = UTC).
func chartTime(minute *int64) *string {
	if minute == nil {
		return nil
	}
	s := time.Unix(*minute*60, 0).UTC().Format("02.01 15:04")
	return &s
}
