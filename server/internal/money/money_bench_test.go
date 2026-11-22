package money

import "testing"

// Микробенчмарки горячих путей денег (аудит PERF-5): разбор сумм из запросов, округления PHP, вывод сумм. Запуск:
// go test -run '^$' -bench . -benchmem ./internal/money/ (сравнение версий — benchstat).

var (
	sinkTiyn  Tiyn
	sinkFloat float64
	sinkStr   string
	sinkInt   int64
)

func BenchmarkParseTenge(b *testing.B) {
	for b.Loop() {
		t, err := ParseTenge("12345.67")
		if err != nil {
			b.Fatal(err)
		}
		sinkTiyn = t
	}
}

func BenchmarkParseTransferAmount(b *testing.B) {
	for b.Loop() {
		sinkTiyn = ParseTransferAmount("1 500,50")
	}
}

func BenchmarkPHPRound(b *testing.B) {
	for b.Loop() {
		sinkFloat = PHPRound(1.005, 2) + PHPRound(0.145*100, 0)
	}
}

func BenchmarkMulDivRound(b *testing.B) {
	for b.Loop() {
		v, err := MulDivRound(123456, 9000, 5000, 10000)
		if err != nil {
			b.Fatal(err)
		}
		sinkInt = v
	}
}

func BenchmarkNumberFormat(b *testing.B) {
	for b.Loop() {
		sinkStr = NumberFormat(123456789, ".", " ")
	}
}
