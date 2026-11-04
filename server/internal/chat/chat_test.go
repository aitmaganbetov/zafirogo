package chat

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

type fakePresence struct {
	asked [][2]int64
	pcs   []presence.State
}

func (f *fakePresence) SignedInOrList(_ context.Context, clubID, userID int64) ([]presence.State, error) {
	f.asked = append(f.asked, [2]int64{clubID, userID})
	return f.pcs, nil
}

type fakeSender struct{ sent []int64 }

func (f *fakeSender) Send(_ context.Context, _, computerID int64, cmd *agentv1.Command) (string, error) {
	if cmd.GetChatMessageReceived() == nil {
		return "", nil
	}
	f.sent = append(f.sent, computerID)
	return "", nil
}

// S7-AB-6: ПК гостя — запросом по гостю (индекс «гость → ПК»), не списком клуба; сообщение — только на ПК в сети,
// где вошёл этот гость (индекс в период прогрева отдаёт и чужие ПК клуба).
func TestDeliverAsksPresenceByGuest(t *testing.T) {
	p := &fakePresence{pcs: []presence.State{
		{ComputerID: 1, SignedInUserID: 7, Online: true},
		{ComputerID: 2, SignedInUserID: 7, Online: false},
		{ComputerID: 3, SignedInUserID: 8, Online: true},
	}}
	s := &fakeSender{}
	Deliver(context.Background(), slog.Default(), s, p, 5, 7, AgentMessage(1, "Админ", "Привет", time.Now()))
	if !slices.Equal(p.asked, [][2]int64{{5, 7}}) {
		t.Fatalf("presence asked %v, want club 5 guest 7", p.asked)
	}
	if !slices.Equal(s.sent, []int64{1}) {
		t.Fatalf("sent to %v, want [1]", s.sent)
	}
}
