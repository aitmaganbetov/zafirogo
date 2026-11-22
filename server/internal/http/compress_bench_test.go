package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
)

// Микробенчмарки ответа карты клуба (аудит PERF-5): JSON состояния 200 ПК (GET /api/club-map/state — опрашивается
// панелью) и он же через middleware сжатия. Запуск: go test -run '^$' -bench ClubMap -benchmem ./internal/http/.

var sinkBody []byte

func benchClubMapState(n int) clubmapapi.ClubMapState {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ends, staff := now.Add(95*time.Minute), "Администратор"
	out := clubmapapi.ClubMapState{Computers: make([]clubmapapi.ComputerState, 0, n)}
	for i := range n {
		c := clubmapapi.ComputerState{Id: int64(1000 + i), Number: int64(i + 1), Name: "PC-" + strconv.Itoa(i+1),
			ZoneId: int64(1 + i%4), Status: "online", Connection: clubmapapi.ComputerStateConnectionOnline,
			AgentState: "session", AgentVersion: "1.4.2", Light: "pc-account", LastSeen: &now,
			Load: &clubmapapi.ComputerLoad{CpuBp: 2300, RamBp: 6100, IdleSeconds: 3}}
		if i%3 == 0 {
			sid, uid := "01HZX"+strconv.Itoa(100000+i), int64(5000+i)
			c.HasSession, c.SessionId, c.SessionUserId, c.SessionEndsAt = true, &sid, &uid, &ends
			c.SessionUser, c.SessionTariff = "Арман · +7 701 *** 12 34", "Ночной пакет"
		}
		if i%10 == 0 {
			c.StaffKind, c.StaffName, c.StaffUntil = "staff", &staff, &ends
		}
		out.Computers = append(out.Computers, c)
	}
	return out
}

func BenchmarkClubMapStateJSON(b *testing.B) {
	st := benchClubMapState(200)
	for b.Loop() {
		out, err := json.Marshal(st)
		if err != nil {
			b.Fatal(err)
		}
		sinkBody = out
	}
}

func BenchmarkClubMapStateCompressed(b *testing.B) {
	body, err := json.Marshal(benchClubMapState(200))
	if err != nil {
		b.Fatal(err)
	}
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/club-map/state", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Encoding") != "gzip" {
			b.Fatal("response is not compressed")
		}
		sinkBody = rec.Body.Bytes()
	}
}
