package monitoring

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

func TestRecorderDrainAndRestore(t *testing.T) {
	now := time.Unix(600*60+30, 0)
	r := NewRecorder(func() time.Time { return now })
	r.Record(ChannelPanel, 7, 120*time.Millisecond, 200)
	r.Record(ChannelPanel, 7, 80*time.Millisecond, 503)
	r.Record(ChannelAgent, 0, 0, 200)
	if d := r.Drain(600); len(d.Channels) != 0 {
		t.Fatalf("open minute drained: %+v", d)
	}
	d := r.Drain(601)
	if got := d.Channels[600][ChannelPanel]; got != (Totals{Requests: 2, Errors: 1, DurationMs: 200}) {
		t.Fatalf("panel: %+v", got)
	}
	if got := d.Clubs[600][7]; got.Panel != 2 || got.Errors != 1 || got.Agent != 0 {
		t.Fatalf("club: %+v", got)
	}
	if _, ok := d.Clubs[600][0]; ok {
		t.Fatal("request without a club counted for a club")
	}
	r.Restore(d)
	if again := r.Drain(601); again.Channels[600][ChannelPanel].Requests != 2 {
		t.Fatalf("restore: %+v", again)
	}
}

func TestChannelsAndMiddleware(t *testing.T) {
	if HTTPChannel("/api/v1/users") != ChannelAPI || HTTPChannel("/api/cash") != ChannelPanel {
		t.Fatal("http channel")
	}
	if GRPCChannel("/zafiro.agent.v1.AgentService/Connect") != ChannelAgent || GRPCChannel("/zafiro.agent.v1.GuestService/Me") != ChannelGuest {
		t.Fatal("grpc channel")
	}
	r := NewRecorder(nil)
	h := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	req := httptest.NewRequest(http.MethodGet, "/api/cash", nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{UserID: 1, ClubID: 9}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	d := r.Drain(time.Now().Unix()/60 + 1)
	var club ClubTotals
	for _, m := range d.Clubs {
		club = m[9]
	}
	if club.Panel != 1 || club.Errors != 1 {
		t.Fatalf("middleware: %+v", d)
	}
}
