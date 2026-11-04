package health

import (
	"reflect"
	"testing"
)

// Вердикт недели (S7-H-15): табличный тест по правилам с порогами по умолчанию.
func TestJudge(t *testing.T) {
	clean := Week{CPUTempAvgDeci: 600, GPUTempAvgDeci: 650, SystemDiskFreeBP: 5000, SmartRank: smartRankOK}
	with := func(f func(*Week)) Week {
		w := clean
		f(&w)
		return w
	}
	type reason struct {
		code, level      string
		value, threshold int64
	}
	cases := []struct {
		name  string
		week  Week
		prev  *Week
		level string
		want  []reason
	}{
		{"clean week", clean, &clean, VerdictOK, nil},
		{"no data at all", Week{CPUTempAvgDeci: -1, GPUTempAvgDeci: -1, SystemDiskFreeBP: -1}, nil, VerdictOK, nil},
		{"smart failing → repair", with(func(w *Week) { w.SmartRank = smartRankFailing }), nil, VerdictRepair,
			[]reason{{ReasonSmartFailing, VerdictRepair, 0, 0}}},
		{"one bugcheck → repair", with(func(w *Week) { w.Bugchecks = 1 }), nil, VerdictRepair,
			[]reason{{ReasonBugchecks, VerdictRepair, 1, 1}}},
		{"cpu hot 2 h — not more than N", with(func(w *Week) { w.CPUHotHours = 2 }), nil, VerdictOK, nil},
		{"cpu hot 3 h → watch", with(func(w *Week) { w.CPUHotHours = 3 }), nil, VerdictWatch,
			[]reason{{ReasonCPUHot, VerdictWatch, 3, 90}}},
		{"cpu hot 25 h → repair", with(func(w *Week) { w.CPUHotHours = 25 }), nil, VerdictRepair,
			[]reason{{ReasonCPUHot, VerdictRepair, 25, 90}}},
		{"gpu hot 24 h → watch", with(func(w *Week) { w.GPUHotHours = 24 }), nil, VerdictWatch,
			[]reason{{ReasonGPUHot, VerdictWatch, 24, 85}}},
		{"smart warning → watch", with(func(w *Week) { w.SmartRank = smartRankWarning }), nil, VerdictWatch,
			[]reason{{ReasonSmartWarning, VerdictWatch, 0, 0}}},
		{"2 shutdowns — fine", with(func(w *Week) { w.Shutdowns = 2 }), nil, VerdictOK, nil},
		{"3 shutdowns → watch", with(func(w *Week) { w.Shutdowns = 3 }), nil, VerdictWatch,
			[]reason{{ReasonShutdowns, VerdictWatch, 3, 3}}},
		{"system disk 9.99 % → watch", with(func(w *Week) { w.SystemDiskFreeBP = 999 }), nil, VerdictWatch,
			[]reason{{ReasonDiskLow, VerdictWatch, 9, 10}}},
		{"system disk exactly 10 % — fine", with(func(w *Week) { w.SystemDiskFreeBP = 1000 }), nil, VerdictOK, nil},
		{"cpu +5 °C to last week → watch", with(func(w *Week) { w.CPUTempAvgDeci = 650 }), &clean, VerdictWatch,
			[]reason{{ReasonCPUTempRise, VerdictWatch, 5, 5}}},
		{"cpu +4.9 °C — fine", with(func(w *Week) { w.CPUTempAvgDeci = 649 }), &clean, VerdictOK, nil},
		{"gpu rise without last week data — no trend", with(func(w *Week) { w.GPUTempAvgDeci = 900 }),
			&Week{CPUTempAvgDeci: -1, GPUTempAvgDeci: -1}, VerdictOK, nil},
		{"rise but no last week at all", with(func(w *Week) { w.CPUTempAvgDeci = 900 }), nil, VerdictOK, nil},
		{"repair wins over watch, reasons in rule order", with(func(w *Week) {
			w.Shutdowns, w.Bugchecks, w.SmartRank = 4, 2, smartRankWarning
		}), nil, VerdictRepair, []reason{{ReasonBugchecks, VerdictRepair, 2, 1}, {ReasonSmartWarning, VerdictWatch, 0, 0},
			{ReasonShutdowns, VerdictWatch, 4, 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Judge(tc.week, tc.prev, VerdictRules{})
			got := []reason{}
			for _, r := range v.Reasons {
				got = append(got, reason{r.Code, r.Level, r.Value, r.Threshold})
			}
			want := tc.want
			if want == nil {
				want = []reason{}
			}
			if v.Level != tc.level || !reflect.DeepEqual(got, want) {
				t.Errorf("Judge = %s %+v, want %s %+v", v.Level, got, tc.level, want)
			}
		})
	}
}

// Пороги из конфигурации меняют правила.
func TestJudgeCustomRules(t *testing.T) {
	r := VerdictRules{CPUTempC: 80, HotHours: 5, HotHoursRepair: 10, Shutdowns: 1, Bugchecks: 3, SystemDiskFreePercent: 20, TempRiseC: 2}
	v := Judge(Week{CPUHotHours: 6, Shutdowns: 1, Bugchecks: 2, SystemDiskFreeBP: 1500, CPUTempAvgDeci: 620},
		&Week{CPUTempAvgDeci: 600}, r)
	codes := []string{}
	for _, x := range v.Reasons {
		codes = append(codes, x.Code)
	}
	if v.Level != VerdictWatch || !reflect.DeepEqual(codes, []string{ReasonCPUHot, ReasonShutdowns, ReasonDiskLow, ReasonCPUTempRise}) ||
		v.Reasons[0].Threshold != 80 {
		t.Errorf("custom rules: %s %+v", v.Level, v.Reasons)
	}
}
