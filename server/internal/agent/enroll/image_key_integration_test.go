//go:build integration

package enroll

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// Решение Q-037 (docs/DECISIONS.md): бездисковые ПК по ключу образа клуба.

var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

func (h *harness) imageKey(t *testing.T, subnets []netip.Prefix) IssuedImageKey {
	t.Helper()
	k, err := h.svc.CreateImageKey(context.Background(), h.staff, ImageKeyRequest{Name: "Образ CCBoot", Subnets: subnets})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (h *harness) enrollImage(t *testing.T, key string, k pcKey, d *agentv1.DeviceInfo) (*agentv1.EnrollResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return agentv1.NewEnrollmentServiceClient(h.dial(t, nil)).Enroll(ctx, &agentv1.EnrollRequest{
		ImageKey: key, CsrPem: k.csr, Device: d})
}

func retryDelay(err error) time.Duration {
	st, _ := status.FromError(err)
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			return ri.GetRetryDelay().AsDuration()
		}
	}
	return 0
}

func TestImageKeyFlow(t *testing.T) {
	h := newHarness(t, `{}`)
	ctx := context.Background()
	key := h.imageKey(t, loopback)
	if !strings.HasPrefix(key.Key, ImageKeyPrefix) || len(key.Key) != 36 || key.Prefix != key.Key[:12] {
		t.Fatalf("key = %+v", key)
	}
	// Ключ — только хешем; в аудите — только начало.
	var leaks int
	mustQueryRow(t, h.d, &leaks, `SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%'`, key.Key[12:])
	if leaks != 0 {
		t.Fatal("image key leaked into audit_log")
	}
	mustQueryRow(t, h.d, &leaks, `SELECT count(*) FROM agent_image_keys WHERE key_prefix = $1 AND octet_length(key_hash) = 32`, key.Prefix)
	if leaks != 1 {
		t.Fatal("image key row")
	}

	// Известный MAC (ПК подключён кодом раньше) — сертификат сразу, 24 часа, прежний отозван.
	known := dev("DL-1", "AA:BB:CC:00:10:01")
	first, err := h.enroll(t, h.code(t, CodeRequest{}), newPCKey(t), known)
	if err != nil {
		t.Fatal(err)
	}
	kk := newPCKey(t)
	resp, err := h.enrollImage(t, key.Key, kk, known)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetComputerId() != first.GetComputerId() || resp.GetClubId() != h.clubID || resp.GetComputerCreated() {
		t.Fatalf("resp = %+v", resp)
	}
	if life := time.Until(resp.GetNotAfter().AsTime()); life > 24*time.Hour+time.Minute || life < 23*time.Hour {
		t.Fatalf("diskless certificate lifetime = %v", life)
	}
	var diskless bool
	var via string
	mustQueryRow(t, h.d, &diskless, `SELECT diskless FROM agent_certificates WHERE club_computer_id = $1 AND revoked_at IS NULL`, resp.GetComputerId())
	mustQueryRow(t, h.d, &via, `SELECT issued_via FROM agent_certificates WHERE club_computer_id = $1 AND revoked_at IS NULL`, resp.GetComputerId())
	if !diskless || via != "image_key" {
		t.Fatalf("certificate diskless=%v via=%q", diskless, via)
	}
	_, err = whoami(t, h.dial(t, kk.cert(t, resp.GetCertificatePem())))
	if err != nil {
		t.Fatal(err)
	}

	// Продление сертификата бездискового ПК — тоже на 24 часа.
	conn := h.dial(t, kk.cert(t, resp.GetCertificatePem()))
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rn, err := agentv1.NewEnrollmentServiceClient(conn).RenewCertificate(rctx, &agentv1.RenewCertificateRequest{
		CsrPem: newPCKey(t).csr, Device: known})
	if err != nil {
		t.Fatal(err)
	}
	if life := time.Until(rn.GetNotAfter().AsTime()); life > 24*time.Hour+time.Minute {
		t.Fatalf("renewed diskless certificate lifetime = %v", life)
	}

	// Новый MAC — PENDING_APPROVAL + RetryInfo, заявка сохраняется; повтор — та же заявка.
	fresh := dev("DL-2", "AA:BB:CC:00:10:02")
	_, err = h.enrollImage(t, key.Key, newPCKey(t), fresh)
	wantReason(t, err, codes.FailedPrecondition, "ERROR_CODE_PENDING_APPROVAL")
	if d := retryDelay(err); d != PendingRetryAfter {
		t.Fatalf("retry delay = %v", d)
	}
	_, err = h.enrollImage(t, key.Key, newPCKey(t), fresh)
	wantReason(t, err, codes.FailedPrecondition, "ERROR_CODE_PENDING_APPROVAL")
	pending, err := h.svc.ListPending(ctx, h.staff)
	if err != nil || len(pending) != 1 || pending[0].MacAddress != "AA:BB:CC:00:10:02" || pending[0].Attempts != 2 ||
		pending[0].Hostname == nil || *pending[0].Hostname != "DESKTOP-DL-2" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	// Подтверждает оператор (роль как у кодов); кассир — нет. Ключи образа оператор не создаёт.
	operator := principal.Principal{UserID: h.staff.UserID, ClubID: h.clubID, Role: "operator"}
	cashier := principal.Principal{UserID: h.staff.UserID, ClubID: h.clubID, Role: "cashier"}
	if _, err := h.svc.ApprovePending(ctx, cashier, ApproveRequest{PendingID: pending[0].ID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cashier approve: %v", err)
	}
	if _, err := h.svc.CreateImageKey(ctx, operator, ImageKeyRequest{Name: "x", Subnets: loopback}); !errors.Is(err, ErrImageKeyForbidden) {
		t.Fatalf("operator creates image key: %v", err)
	}
	if _, err := h.svc.ListImageKeys(ctx, principal.Principal{UserID: h.staff.UserID, ClubID: h.clubID, Role: "owner", ReadOnly: true}); !errors.Is(err, ErrImageKeyForbidden) {
		t.Fatalf("read-only view lists image keys: %v", err)
	}
	pcID, err := h.svc.ApprovePending(ctx, operator, ApproveRequest{PendingID: pending[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApprovePending(ctx, operator, ApproveRequest{PendingID: pending[0].ID}); !errors.Is(err, ErrPendingNotFound) {
		t.Fatalf("approve twice: %v", err)
	}
	var mac, zone string
	mustQueryRow(t, h.d, &mac, `SELECT mac_address FROM club_computers WHERE id = $1`, pcID)
	mustQueryRow(t, h.d, &zone, `SELECT z.name FROM club_computers c JOIN club_zones z ON z.id = c.club_zone_id WHERE c.id = $1`, pcID)
	if mac != "AA:BB:CC:00:10:02" || zone != "Не назначена" {
		t.Fatalf("approved card mac=%q zone=%q", mac, zone)
	}
	resp2, err := h.enrollImage(t, key.Key, newPCKey(t), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.GetComputerId() != pcID {
		t.Fatalf("computer = %d, want %d", resp2.GetComputerId(), pcID)
	}
	if left, _ := h.svc.ListPending(ctx, h.staff); len(left) != 0 {
		t.Fatalf("pending after approve: %+v", left)
	}

	// Отклонённый ПК получает отказ.
	rejected := dev("DL-3", "AA:BB:CC:00:10:03")
	_, err = h.enrollImage(t, key.Key, newPCKey(t), rejected)
	wantReason(t, err, codes.FailedPrecondition, "ERROR_CODE_PENDING_APPROVAL")
	pending, _ = h.svc.ListPending(ctx, h.staff)
	if err := h.svc.RejectPending(ctx, h.staff, pending[0].ID, netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	_, err = h.enrollImage(t, key.Key, newPCKey(t), rejected)
	wantReason(t, err, codes.PermissionDenied, "ERROR_CODE_ENROLLMENT_CODE_INVALID")

	// Отзыв ключа отзывает выданные по нему сертификаты; новый Enroll и продление — отказ.
	keys, err := h.svc.ListImageKeys(ctx, h.staff)
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	n, err := h.svc.RevokeImageKey(ctx, h.staff, key.ID, netip.Addr{})
	if err != nil || n != 3 { // DL-1 (исходный и продлённый — новый ещё не использован) и DL-2
		t.Fatalf("revoked = %d, %v", n, err)
	}
	if _, err := h.svc.RevokeImageKey(ctx, h.staff, key.ID, netip.Addr{}); !errors.Is(err, ErrImageKeyNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	_, err = h.enrollImage(t, key.Key, newPCKey(t), dev("DL-1b", "AA:BB:CC:00:10:01"))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_EXPIRED")
	var actions int
	mustQueryRow(t, h.d, &actions, `SELECT count(DISTINCT action) FROM audit_log WHERE club_id = $1 AND action IN
		('agent.image_key.created', 'agent.image_key.revoked', 'agent.pending_computer.created',
		 'agent.pending_computer.approved', 'agent.pending_computer.rejected')`, h.clubID)
	if actions != 5 {
		t.Fatalf("audit actions = %d", actions)
	}
}

// Ключ действует только из своих подсетей; неверный ключ, оба секрета, нет MAC — отказ.
func TestImageKeyRejects(t *testing.T) {
	h := newHarness(t, `{}`)
	ctx := context.Background()
	far := h.imageKey(t, []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")})
	_, err := h.enrollImage(t, far.Key, newPCKey(t), dev("RJ-1", "AA:BB:CC:00:11:01"))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID")
	if st, _ := status.FromError(err); st.Message() != ErrImageKeyNetwork.Message {
		t.Fatalf("message = %q", st.Message())
	}
	_, err = h.enrollImage(t, "zik_"+strings.Repeat("A", 32), newPCKey(t), dev("RJ-2", "AA:BB:CC:00:11:02"))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID")
	_, err = h.enrollImage(t, "not-a-key", newPCKey(t), dev("RJ-3", "AA:BB:CC:00:11:03"))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_INVALID")

	key := h.imageKey(t, loopback)
	_, err = h.enrollImage(t, key.Key, newPCKey(t), dev("RJ-4", ""))
	wantReason(t, err, codes.InvalidArgument, "ERROR_CODE_VALIDATION_FAILED")
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = agentv1.NewEnrollmentServiceClient(h.dial(t, nil)).Enroll(cctx, &agentv1.EnrollRequest{
		ImageKey: key.Key, EnrollmentCode: "ABCD-EFGH", CsrPem: newPCKey(t).csr, Device: dev("RJ-5", "AA:BB:CC:00:11:05")})
	wantReason(t, err, codes.InvalidArgument, "ERROR_CODE_VALIDATION_FAILED")

	// Истёкший ключ.
	mustExec(t, h.d, `UPDATE agent_image_keys SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = $1`, key.ID)
	_, err = h.enrollImage(t, key.Key, newPCKey(t), dev("RJ-6", "AA:BB:CC:00:11:06"))
	wantReason(t, err, codes.Unauthenticated, "ERROR_CODE_ENROLLMENT_CODE_EXPIRED")

	// Проверки ключа в панели.
	bad := []ImageKeyRequest{
		{Name: "", Subnets: loopback},
		{Name: "x"},
		{Name: "x", Subnets: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
		{Name: "x", Subnets: loopback, ExpiresAt: new(time.Now().Add(-time.Minute))},
	}
	for i, req := range bad {
		var e *Error
		if _, err := h.svc.CreateImageKey(ctx, h.staff, req); !errors.As(err, &e) || e.Kind != KindInvalid {
			t.Errorf("bad[%d]: %v", i, err)
		}
	}
}

// Ключ образа чужого клуба не виден ни через RLS, ни через поиск по хешу без самого ключа.
func TestImageKeyRLS(t *testing.T) {
	h := newHarness(t, `{}`)
	h.imageKey(t, loopback)
	var n int
	if err := h.d.App.QueryRow(context.Background(), `SELECT count(*) FROM agent_image_keys`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("image keys without club: %d %v", n, err)
	}
	if err := h.d.App.QueryRow(context.Background(), `SELECT count(*) FROM agent_pending_computers`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending without club: %d %v", n, err)
	}
}
