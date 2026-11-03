//go:build integration

package presence

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/valkey-io/valkey-go"
)

// Образ — как в docker-compose.dev.yml.
const valkeyImage = "valkey/valkey:9.1.3-alpine"

var testValkey valkey.Client

func TestMain(m *testing.M) {
	ctx := context.Background()
	vk, err := testcontainers.Run(ctx, valkeyImage, testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "presence integration env:", err)
		os.Exit(1)
	}
	addr, err := vk.PortEndpoint(ctx, "6379/tcp", "")
	if err == nil {
		testValkey, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "presence integration env:", err)
		_ = testcontainers.TerminateContainer(vk)
		os.Exit(1)
	}
	code := m.Run()
	testValkey.Close()
	_ = testcontainers.TerminateContainer(vk)
	os.Exit(code)
}

var prefixSeq int

func newTestStore(tb testing.TB) *Store {
	tb.Helper()
	prefixSeq++
	return NewStore(testValkey, "t"+strconv.Itoa(prefixSeq)+":"+strconv.FormatInt(time.Now().UnixNano(), 36)+":", 90*time.Second)
}

// readyIndex — метка индекса старше прогрева (как через guestIndexWarmup после выкладки).
func readyIndex(tb testing.TB, s *Store) {
	tb.Helper()
	old := strconv.FormatInt(time.Now().Add(-2*guestIndexWarmup).UnixMilli(), 10)
	if err := testValkey.Do(context.Background(), testValkey.B().Set().Key(s.guestSinceKey()).Value(old).Build()).Error(); err != nil {
		tb.Fatal(err)
	}
}

// put — ПК на связи со снимком (гость user; 0 — никого).
func put(tb testing.TB, s *Store, club, pc, user int64) Owner {
	tb.Helper()
	ctx := context.Background()
	own, _, _, err := s.Acquire(ctx, club, pc, "i1")
	if err != nil {
		tb.Fatal(err)
	}
	st := State{ClubID: club, ComputerID: pc, Online: true, Instance: own.Instance, Gen: own.Gen, SignedInUserID: user,
		AgentState: "locked", LastSeenAt: time.Now().UTC()}
	if err := s.Refresh(ctx, RefreshItem{Owner: own, State: &st, ClubID: club, ComputerID: pc}); err != nil {
		tb.Fatal(err)
	}
	return own
}

func signedInPCs(tb testing.TB, s *Store, club, user int64) ([]int64, bool) {
	tb.Helper()
	sts, ready, err := s.SignedIn(context.Background(), club, user)
	if err != nil {
		tb.Fatal(err)
	}
	var out []int64
	for _, st := range sts {
		out = append(out, st.ComputerID)
	}
	return out, ready
}

func (s *Store) guestMembers(tb testing.TB, club, user int64) int64 {
	tb.Helper()
	n, err := testValkey.Do(context.Background(), testValkey.B().Scard().Key(s.guestKey(club, user)).Build()).AsInt64()
	if err != nil {
		tb.Fatal(err)
	}
	return n
}

// S6-O4-11: индекс «гость → ПК» поддерживается записью снимка (вход, смена гостя, смена ПК, закрытие стрима,
// уборка Sweeper) и сам себя чистит при чтении; до конца прогрева — ready = false (вызывающий читает List).
func TestGuestIndex(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const club, other = 10, 11

	// Первая запись снимка ставит метку; индексу ещё не доверяют.
	put(t, s, club, 1, 100)
	if _, ready := signedInPCs(t, s, club, 100); ready {
		t.Fatal("fresh index must not be ready")
	}
	if ttl, err := testValkey.Do(ctx, testValkey.B().Pttl().Key(s.guestSinceKey()).Build()).AsInt64(); err != nil || ttl <= 0 {
		t.Fatalf("marker ttl = %d, %v", ttl, err)
	}
	readyIndex(t, s)
	if pcs, ready := signedInPCs(t, s, club, 100); !ready || len(pcs) != 1 || pcs[0] != 1 {
		t.Fatalf("signed in = %v (ready %v)", pcs, ready)
	}
	// Тот же гость в другом клубе (тот же номер ПК невозможен, но индекс — по клубу) — не путается.
	put(t, s, other, 2, 100)
	if pcs, _ := signedInPCs(t, s, club, 100); len(pcs) != 1 || pcs[0] != 1 {
		t.Fatalf("other club leaked: %v", pcs)
	}

	// На ПК 1 вошёл другой гость: прежнему — ничего, ПК убран из его индекса при чтении.
	put(t, s, club, 1, 200)
	if pcs, _ := signedInPCs(t, s, club, 100); len(pcs) != 0 {
		t.Fatalf("old guest still on pc 1: %v", pcs)
	}
	if n := s.guestMembers(t, club, 100); n != 0 {
		t.Fatalf("stale index members: %d", n)
	}
	// Гость 200 перешёл на ПК 3 (ПК 1 — никого).
	put(t, s, club, 1, 0)
	put(t, s, club, 3, 200)
	if pcs, _ := signedInPCs(t, s, club, 200); len(pcs) != 1 || pcs[0] != 3 {
		t.Fatalf("moved guest = %v", pcs)
	}

	// Стрим ПК 3 закрыт: снимок с гостем остаётся до конца окна (Online = false) — SignedIn его видит, как List.
	own3, _, err := s.OwnerOf(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Release(ctx, own3, State{ClubID: club, ComputerID: 3, SignedInUserID: 200, LastSeenAt: time.Now().UTC()}, time.Millisecond); err != nil || !ok {
		t.Fatalf("release = %v, %v", ok, err)
	}
	sts, _, err := s.SignedIn(ctx, club, 200)
	if err != nil || len(sts) != 1 || sts[0].Online {
		t.Fatalf("released = %+v, %v", sts, err)
	}
	// Окно вышло, Sweeper забрал ПК: снимка нет — нет и ПК в индексе.
	time.Sleep(5 * time.Millisecond)
	if _, err := s.ClaimGone(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if pcs, _ := signedInPCs(t, s, club, 200); len(pcs) != 0 {
		t.Fatalf("gone pc still listed: %v", pcs)
	}
	if n := s.guestMembers(t, club, 200); n != 0 {
		t.Fatalf("stale index members after sweep: %d", n)
	}

	// Переподключение на другой инстанс: новый владелец пишет снимок — индекс тот же.
	put(t, s, club, 4, 300)
	own, _, _, err := s.Acquire(ctx, club, 4, "i2")
	if err != nil {
		t.Fatal(err)
	}
	st := State{ClubID: club, ComputerID: 4, Online: true, Instance: "i2", Gen: own.Gen, SignedInUserID: 300, LastSeenAt: time.Now().UTC()}
	if err := s.Refresh(ctx, RefreshItem{Owner: own, State: &st, ClubID: club, ComputerID: 4}); err != nil {
		t.Fatal(err)
	}
	if sts, _, err := s.SignedIn(ctx, club, 300); err != nil || len(sts) != 1 || sts[0].Instance != "i2" {
		t.Fatalf("after reconnect = %+v, %v", sts, err)
	}

	// SignedInOrList: пока индекс прогревается — из List (ListCalls растёт), потом — без List.
	s2 := newTestStore(t)
	put(t, s2, club, 5, 400)
	if sts, err := s2.SignedInOrList(ctx, club, 400); err != nil || len(sts) != 1 || s2.ListCalls() != 1 {
		t.Fatalf("warm-up fallback = %+v, %v, lists %d", sts, err, s2.ListCalls())
	}
	readyIndex(t, s2)
	if sts, err := s2.SignedInOrList(ctx, club, 400); err != nil || len(sts) != 1 || s2.ListCalls() != 1 {
		t.Fatalf("ready = %+v, %v, lists %d", sts, err, s2.ListCalls())
	}
	// Индекс пропал (потеря данных Valkey, откат версии) — снова прогрев, а не «никого нет».
	if err := testValkey.Do(ctx, testValkey.B().Del().Key(s2.guestSinceKey(), s2.guestKey(club, 400)).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	if sts, err := s2.SignedInOrList(ctx, club, 400); err != nil || len(sts) != 1 || s2.ListCalls() != 2 {
		t.Fatalf("lost index = %+v, %v, lists %d", sts, err, s2.ListCalls())
	}
}

// S6-O4-11, замер (docs/load-test.md): ПК гостя в клубе на 500 ПК — прежний путь (List: HGETALL + JSON всех ПК) и
// индекс (SignedIn). go test -tags=integration -run '^$' -bench GuestLookup ./internal/agent/presence/
func BenchmarkGuestLookup(b *testing.B) {
	s := newTestStore(b)
	const club, pcs, user = 20, 500, 7777
	for pc := int64(1); pc <= pcs; pc++ {
		u := int64(0)
		if pc%2 == 0 {
			u = 10000 + pc // половина ПК занята другими гостями
		}
		if pc == 250 {
			u = user
		}
		put(b, s, club, pc, u)
	}
	readyIndex(b, s)
	ctx := context.Background()
	b.Run("List", func(b *testing.B) {
		for b.Loop() {
			sts, err := s.List(ctx, club)
			if err != nil {
				b.Fatal(err)
			}
			n := 0
			for _, st := range sts {
				if st.SignedInUserID == user {
					n++
				}
			}
			if n != 1 {
				b.Fatalf("found %d", n)
			}
		}
	})
	b.Run("Index", func(b *testing.B) {
		for b.Loop() {
			sts, ready, err := s.SignedIn(ctx, club, user)
			if err != nil || !ready || len(sts) != 1 {
				b.Fatalf("found %d, ready %v, %v", len(sts), ready, err)
			}
		}
	})
}
