package enroll

// Бездисковые ПК: регистрация по многоразовому «ключу образа» клуба.
//
// Решение Q-037 (docs/DECISIONS.md): ключ образа хранится хешем (HMAC с тем же секретом, что коды), отзывается,
// действует только из подсетей клуба. ПК с известным клубу MAC получает сертификат сразу (24 ч), новый MAC
// попадает в «ожидающие» и требует подтверждения сотрудником. Протокол — docs/agent-protocol.md §12.8.
//
// Старый аналог — постоянный ключ установки клуба (app/Services/ClubInstallationKey.php,
// app/Http/Controllers/Api/AgentEnrollController.php): неизвестные ПК добавлялись в «Не назначена» без
// подтверждения, ключ нельзя было отозвать.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Ключ образа и ожидающие ПК.
const (
	// ImageKeyPrefix — начало ключа образа ("zik_" + 32 символа base64url = 192 бита).
	ImageKeyPrefix = "zik_"
	imageKeyBytes  = 24
	// imageKeyShown — сколько символов ключа хранится и показывается в панели (как installation_key_prefix).
	imageKeyShown = 12
	// DefaultDisklessCertLifetime — срок сертификата бездискового ПК (решение Q-037).
	DefaultDisklessCertLifetime = 24 * time.Hour
	// PendingRetryAfter — через сколько ожидающий подтверждения ПК повторяет Enroll (RetryInfo).
	PendingRetryAfter = 30 * time.Second
	// maxWaitingPending — нерешённых заявок на клуб: утёкший ключ не должен заваливать панель.
	maxWaitingPending  = 200
	maxImageKeySubnets = 16
	maxImageKeyName    = 100
)

// ImageKeyRoles — кто создаёт, видит и отзывает ключи образа: как «Ключ установки клуба» в старом
// (resources/views/settings/club.blade.php — полный ключ видят только owner и admin). Подтверждают ожидающие
// ПК роли RegisterRoles (как коды регистрации).
var ImageKeyRoles = []string{"owner", "admin"}

// CanManageImageKeys — может ли сотрудник создавать, смотреть и отзывать ключи образа.
func CanManageImageKeys(p principal.Principal) bool {
	return p.ClubID > 0 && !p.ReadOnly && slices.Contains(ImageKeyRoles, p.Role)
}

// newImageKey — случайный ключ образа.
func newImageKey() (string, error) {
	buf := make([]byte, imageKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return ImageKeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// NormalizeImageKey — ключ без пробелов по краям; "" — не ключ образа.
func NormalizeImageKey(s string) string {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, ImageKeyPrefix)
	if !ok || len(rest) != base64.RawURLEncoding.EncodedLen(imageKeyBytes) {
		return ""
	}
	if _, err := base64.RawURLEncoding.DecodeString(rest); err != nil {
		return ""
	}
	return s
}

func lookupImageKey(ctx context.Context, q *store.Queries, hash []byte) (int64, int64, error) {
	k, err := q.LookupImageKey(ctx, hash)
	if err != nil {
		return 0, 0, notFound(err, ErrImageKeyInvalid)
	}
	return k.ID, k.ClubID, nil
}

// inSubnets — входит ли адрес ПК в одну из подсетей ключа.
func inSubnets(ip netip.Addr, subnets []netip.Prefix) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, p := range subnets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// checkImageKey — ключ не отозван, не истёк, IP из его подсетей (Enroll и продление сертификата бездискового ПК).
func (s *Service) checkImageKey(ctx context.Context, q *store.Queries, clubID int64, keyID *int64, ip netip.Addr) error {
	if keyID == nil {
		return ErrImageKeyExpired
	}
	k, err := q.GetImageKeyForEnroll(ctx, store.GetImageKeyForEnrollParams{ClubID: clubID, ID: *keyID})
	if err != nil {
		return notFound(err, ErrImageKeyExpired)
	}
	if k.RevokedAt != nil || (k.ExpiresAt != nil && !k.ExpiresAt.After(s.opts.Now())) {
		return ErrImageKeyExpired
	}
	if !inSubnets(ip, k.Subnets) {
		return ErrImageKeyNetwork
	}
	return nil
}

// enrollByImageKey — Enroll бездискового ПК. Лимиты частоты уже проверены (как у кодов).
func (s *Service) enrollByImageKey(ctx context.Context, in EnrollInput, dev device, failKey string) (*agentv1.EnrollResponse, error) {
	key := NormalizeImageKey(in.ImageKey)
	if key == "" {
		return nil, s.codeFailure(ctx, failKey, ErrImageKeyInvalid)
	}
	if dev.mac == "" {
		return nil, ErrMACRequired
	}
	csr, err := pki.ParseCSR([]byte(in.CSRPEM))
	if err != nil {
		return nil, ErrCSRInvalid
	}
	var (
		resp    *agentv1.EnrollResponse
		issued  pki.Issued
		clubID  int64
		pcID    int64
		revoked []string
		pending bool
	)
	err = s.lookupTx(ctx, hashCode(s.opts.CodePepper, key), lookupImageKey, func(q *store.Queries, keyID, club int64) error {
		clubID, pending, revoked = club, false, nil
		if err := s.checkImageKey(ctx, q, club, &keyID, in.IP); err != nil {
			return err
		}
		clubRow, err := q.GetClubForImageEnroll(ctx, club)
		if err != nil {
			return err
		}
		sub, err := q.GetClubSubscriptionForAgent(ctx, club)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSubscriptionInactive
		}
		if err != nil {
			return err
		}
		if !subscriptionActive(sub.Status, sub.TrialEndsAt, sub.CurrentPeriodEndsAt, s.opts.Now()) {
			return ErrSubscriptionInactive
		}
		r, err := q.FindComputerByMac(ctx, store.FindComputerByMacParams{ClubID: club, MacAddress: &dev.mac})
		if errors.Is(err, pgx.ErrNoRows) {
			// Новый MAC — в «ожидающие» (заявка сохраняется, ответ — PENDING_APPROVAL после COMMIT).
			pending = true
			return s.savePending(ctx, q, club, keyID, dev, in.IP)
		}
		if err != nil {
			return err
		}
		pc := card(r)
		pcID = pc.ID
		if err := s.updateHardware(ctx, q, club, pc.ID, dev, in.IP); err != nil {
			return err
		}
		if revoked, err = q.RevokeComputerCertificates(ctx, store.RevokeComputerCertificatesParams{
			Reason: "reenrolled", ClubID: club, ClubComputerID: pc.ID}); err != nil {
			return err
		}
		if issued, err = s.opts.CA.Issue(pki.IssueRequest{CSR: csr, ClubID: club, ComputerID: pc.ID,
			Lifetime: s.opts.DisklessCertLifetime}); err != nil {
			return err
		}
		if err := q.InsertAgentCertificate(ctx, store.InsertAgentCertificateParams{
			Serial: issued.Serial, ClubID: club, ClubComputerID: pc.ID, SpkiSha256: issued.SPKISHA256,
			NotBefore: issued.NotBefore, NotAfter: issued.NotAfter, IssuedVia: "image_key",
			MacAddress: ptr(dev.mac), HardwareUuid: ptr(dev.uuid), Diskless: true, ImageKeyID: &keyID,
		}); err != nil {
			return err
		}
		if err := q.TouchImageKey(ctx, store.TouchImageKeyParams{ClubID: club, ID: keyID}); err != nil {
			return err
		}
		if err := s.audit(ctx, q, club, nil, "agent", "agent.enrolled", &pc.ID, in.IP, map[string]any{
			"serial": issued.Serial, "not_after": issued.NotAfter, "image_key_id": keyID, "diskless": true,
			"revoked": revoked, "mac_address": dev.mac, "hardware_uuid": dev.uuid,
		}); err != nil {
			return err
		}
		resp = &agentv1.EnrollResponse{
			CertificatePem: issued.CertPEM, CaChainPem: s.opts.CA.ChainPEM(), ComputerId: pc.ID, ClubId: club,
			ClubName: clubRow.Name, ComputerNumber: strconv.FormatInt(pc.Number, 10),
			NotAfter: timestamppb.New(issued.NotAfter), RenewAfter: timestamppb.New(issued.RenewAfter),
			AgentEndpoint: s.opts.AgentEndpoint,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrImageKeyInvalid) || errors.Is(err, ErrImageKeyExpired) || errors.Is(err, ErrImageKeyNetwork) {
			return nil, s.codeFailure(ctx, failKey, err)
		}
		return nil, err
	}
	if pending {
		return nil, ErrPendingApproval
	}
	if s.opts.Cache != nil {
		s.opts.Cache.Add(issued.Serial, clubID, pcID, issued.NotAfter, false)
	}
	s.afterRevoke(ctx, clubID, pcID, revoked, true)
	s.opts.Logger.InfoContext(ctx, "agent enrolled by image key", "club_id", clubID, "computer_id", pcID,
		"serial", issued.Serial)
	return resp, nil
}

// savePending — заявка ПК с новым MAC: новая или обновление существующей. Отклонённый ПК получает отказ.
func (s *Service) savePending(ctx context.Context, q *store.Queries, clubID, keyID int64, dev device, ip netip.Addr) error {
	info, err := protojson.Marshal(dev.info)
	if err != nil {
		return err
	}
	p, err := q.GetOpenPendingComputer(ctx, store.GetOpenPendingComputerParams{ClubID: clubID, MacAddress: dev.mac})
	switch {
	case err == nil && p.RejectedAt != nil:
		return ErrComputerRejected
	case err == nil:
		return q.TouchPendingComputer(ctx, store.TouchPendingComputerParams{
			Ip: addrPtr(ip), ImageKeyID: &keyID, HardwareUuid: ptr(dev.uuid), Hostname: ptr(dev.name), Device: info,
			ClubID: clubID, ID: p.ID,
		})
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	waiting, err := q.CountWaitingPendingComputers(ctx, clubID)
	if err != nil {
		return err
	}
	if waiting >= maxWaitingPending {
		return ErrTooManyPending
	}
	row, err := q.InsertPendingComputer(ctx, store.InsertPendingComputerParams{
		ClubID: clubID, ImageKeyID: &keyID, MacAddress: dev.mac, HardwareUuid: ptr(dev.uuid), Hostname: ptr(dev.name),
		Device: info, Ip: addrPtr(ip),
	})
	if err != nil || !row.Inserted {
		return err
	}
	return s.audit(ctx, q, clubID, nil, "agent", "agent.pending_computer.created", nil, ip, map[string]any{
		"pending_id": row.ID, "image_key_id": keyID, "mac_address": dev.mac, "hardware_uuid": dev.uuid,
	})
}

// ---------------------------------------------------------------------------
// Панель: ключи образа
// ---------------------------------------------------------------------------

// ImageKeyRequest — новый ключ образа.
type ImageKeyRequest struct {
	// Name — подпись в панели (1..100 символов).
	Name string
	// Subnets — подсети, из которых ключ действует (1..16; адрес, который видит сервер).
	Subnets []netip.Prefix
	// ExpiresAt — срок (nil — до отзыва).
	ExpiresAt *time.Time
	// IP — адрес сотрудника (аудит).
	IP netip.Addr
}

// IssuedImageKey — созданный ключ. Key показывается один раз и нигде не хранится.
type IssuedImageKey struct {
	ID        int64
	Key       string
	Prefix    string
	Name      string
	Subnets   []netip.Prefix
	CreatedAt time.Time
	ExpiresAt *time.Time
}

func (s *Service) checkImageKeyRequest(req ImageKeyRequest) (string, []netip.Prefix, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", nil, validation("name", "Укажите название ключа.")
	}
	if utf8.RuneCountInString(name) > maxImageKeyName {
		return "", nil, validation("name", "Слишком длинное значение.")
	}
	if len(req.Subnets) == 0 {
		return "", nil, validation("subnets", "Укажите хотя бы одну подсеть.")
	}
	if len(req.Subnets) > maxImageKeySubnets {
		return "", nil, validation("subnets", "Слишком много подсетей.")
	}
	subnets := make([]netip.Prefix, 0, len(req.Subnets))
	for _, p := range req.Subnets {
		if !p.IsValid() || p.Bits() == 0 || p.Addr().Is4In6() || p.Addr().Zone() != "" {
			// 0.0.0.0/0 и ::/0 не ограничивают ничего — ограничение подсетью обязательно (Q-037).
			return "", nil, validation("subnets", "Подсеть должна ограничивать адреса клуба.")
		}
		p = p.Masked()
		if !slices.Contains(subnets, p) {
			subnets = append(subnets, p)
		}
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(s.opts.Now()) {
		return "", nil, validation("expires_at", "Срок должен быть в будущем.")
	}
	return name, subnets, nil
}

// CreateImageKey — «Создать ключ образа»: многоразовый ключ для бездисковых ПК клуба.
func (s *Service) CreateImageKey(ctx context.Context, p principal.Principal, req ImageKeyRequest) (IssuedImageKey, error) {
	if !CanManageImageKeys(p) {
		return IssuedImageKey{}, ErrImageKeyForbidden
	}
	name, subnets, err := s.checkImageKeyRequest(req)
	if err != nil {
		return IssuedImageKey{}, err
	}
	var out IssuedImageKey
	err = s.withKeyRetry(func() error {
		key, err := newImageKey()
		if err != nil {
			return err
		}
		return db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
			row, err := q.CreateImageKey(ctx, store.CreateImageKeyParams{
				ClubID: p.ClubID, KeyHash: hashCode(s.opts.CodePepper, key), KeyPrefix: key[:imageKeyShown], Name: name,
				Subnets: subnets, CreatedByUserID: ptr(p.UserID), ExpiresAt: req.ExpiresAt,
			})
			if err != nil {
				return err
			}
			// Сам ключ в аудит не попадает (секрет) — только начало, как в панели.
			if err := s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.image_key.created", nil, req.IP,
				map[string]any{"image_key_id": row.ID, "key_prefix": key[:imageKeyShown], "name": name,
					"subnets": prefixStrings(subnets), "expires_at": req.ExpiresAt}); err != nil {
				return err
			}
			out = IssuedImageKey{ID: row.ID, Key: key, Prefix: key[:imageKeyShown], Name: name, Subnets: subnets,
				CreatedAt: row.CreatedAt, ExpiresAt: req.ExpiresAt}
			return nil
		})
	})
	return out, err
}

// RevokeImageKey — «Отозвать ключ образа»: ключ перестаёт действовать, сертификаты, выданные по нему,
// отзываются сразу (ПК теряют связь и не могут зарегистрироваться снова). Возвращает число отозванных сертификатов.
func (s *Service) RevokeImageKey(ctx context.Context, p principal.Principal, keyID int64, ip netip.Addr) (int, error) {
	if !CanManageImageKeys(p) {
		return 0, ErrImageKeyForbidden
	}
	var certs []store.RevokeImageKeyCertificatesRow
	err := db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
		k, err := q.RevokeImageKey(ctx, store.RevokeImageKeyParams{RevokedByUserID: ptr(p.UserID), ClubID: p.ClubID, ID: keyID})
		if err != nil {
			return notFound(err, ErrImageKeyNotFound)
		}
		if certs, err = q.RevokeImageKeyCertificates(ctx, store.RevokeImageKeyCertificatesParams{
			RevokedByUserID: ptr(p.UserID), ClubID: p.ClubID, ImageKeyID: &keyID}); err != nil {
			return err
		}
		serials := make([]string, len(certs))
		for i, c := range certs {
			serials[i] = c.Serial
		}
		return s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.image_key.revoked", nil, ip,
			map[string]any{"image_key_id": k.ID, "key_prefix": k.KeyPrefix, "name": k.Name, "serials": serials})
	})
	if err != nil {
		return 0, err
	}
	byComputer := map[int64][]string{}
	for _, c := range certs {
		byComputer[c.ClubComputerID] = append(byComputer[c.ClubComputerID], c.Serial)
	}
	for computerID, serials := range byComputer {
		s.afterRevoke(ctx, p.ClubID, computerID, serials, true)
	}
	return len(certs), nil
}

// ListImageKeys — ключи образа клуба (без самих ключей), включая отозванные.
func (s *Service) ListImageKeys(ctx context.Context, p principal.Principal) ([]store.ListImageKeysRow, error) {
	if !CanManageImageKeys(p) {
		return nil, ErrImageKeyForbidden
	}
	var rows []store.ListImageKeysRow
	err := db.InTenantTx(ctx, s.opts.Pool, p.ClubID, func(q *store.Queries) error {
		var err error
		rows, err = q.ListImageKeys(ctx, p.ClubID)
		return err
	})
	return rows, err
}

// ---------------------------------------------------------------------------
// Панель: ожидающие подтверждения ПК
// ---------------------------------------------------------------------------

// ListPending — ПК, ожидающие подтверждения (роли RegisterRoles, как коды регистрации).
func (s *Service) ListPending(ctx context.Context, p principal.Principal) ([]store.ListWaitingPendingComputersRow, error) {
	if !CanRegister(p) {
		return nil, ErrForbidden
	}
	var rows []store.ListWaitingPendingComputersRow
	err := db.InTenantTx(ctx, s.opts.Pool, p.ClubID, func(q *store.Queries) error {
		var err error
		rows, err = q.ListWaitingPendingComputers(ctx, p.ClubID)
		return err
	})
	return rows, err
}

// ApproveRequest — подтверждение ожидающего ПК.
type ApproveRequest struct {
	PendingID int64
	// ZoneID — зона новой карточки (nil — «Не назначена», как у кодов).
	ZoneID *int64
	// IP — адрес сотрудника (аудит).
	IP netip.Addr
}

// ApprovePending — «Подтвердить ПК»: создаёт карточку ПК (ClubComputerRegistrar::register: лимит ПК тарифа,
// вместимость зоны, номер) или берёт уже существующую с этим MAC. Следующий Enroll ПК по ключу образа выдаст
// сертификат. Можно подтвердить и ранее отклонённый ПК. Возвращает id карточки.
func (s *Service) ApprovePending(ctx context.Context, p principal.Principal, req ApproveRequest) (int64, error) {
	if !CanRegister(p) {
		return 0, ErrForbidden
	}
	var computerID int64
	err := db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
		row, err := q.GetPendingComputerForDecision(ctx, store.GetPendingComputerForDecisionParams{ClubID: p.ClubID, ID: req.PendingID})
		if err != nil {
			return notFound(err, ErrPendingNotFound)
		}
		if _, err := q.LockClubForEnroll(ctx, p.ClubID); err != nil {
			return err
		}
		dev := pendingDevice(row)
		created := false
		existing, err := q.FindComputerByMac(ctx, store.FindComputerByMacParams{ClubID: p.ClubID, MacAddress: &dev.mac})
		switch {
		case err == nil:
			computerID = existing.ID
		case errors.Is(err, pgx.ErrNoRows):
			var features []byte
			sub, err := q.GetClubSubscriptionForAgent(ctx, p.ClubID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				features = sub.Features
			}
			pc, err := s.register(ctx, q, p.ClubID, store.ConsumeEnrollmentCodeRow{
				ClubZoneID: req.ZoneID, CreatedByUserID: ptr(p.UserID)}, dev, features)
			if err != nil {
				return err
			}
			computerID, created = pc.ID, true
		default:
			return err
		}
		if err := q.ApprovePendingComputer(ctx, store.ApprovePendingComputerParams{
			ApprovedByUserID: ptr(p.UserID), ClubComputerID: &computerID, ClubID: p.ClubID, ID: row.ID}); err != nil {
			return err
		}
		return s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.pending_computer.approved", &computerID, req.IP,
			map[string]any{"pending_id": row.ID, "mac_address": row.MacAddress, "computer_created": created,
				"zone_id": req.ZoneID})
	})
	return computerID, err
}

// RejectPending — «Отклонить ПК»: Enroll этого MAC по ключу образа получает отказ, пока ПК не подтвердят.
func (s *Service) RejectPending(ctx context.Context, p principal.Principal, pendingID int64, ip netip.Addr) error {
	if !CanRegister(p) {
		return ErrForbidden
	}
	return db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
		n, err := q.RejectPendingComputer(ctx, store.RejectPendingComputerParams{
			RejectedByUserID: ptr(p.UserID), ClubID: p.ClubID, ID: pendingID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrPendingNotFound
		}
		return s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.pending_computer.rejected", nil, ip,
			map[string]any{"pending_id": pendingID})
	})
}

// pendingDevice — сведения о ПК из заявки (как checkDevice при Enroll).
func pendingDevice(row store.GetPendingComputerForDecisionRow) device {
	info := &agentv1.DeviceInfo{}
	if len(row.Device) > 0 {
		_ = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(row.Device, info)
	}
	name := ""
	if row.Hostname != nil {
		name = strings.TrimSpace(*row.Hostname)
	}
	if name == "" {
		name = "PC"
	}
	uuid := ""
	if row.HardwareUuid != nil {
		uuid = *row.HardwareUuid
	}
	return device{uuid: uuid, mac: row.MacAddress, name: name, info: info}
}

// withKeyRetry — повтор при совпадении хеша ключа (практически невозможно при 192 битах).
func (s *Service) withKeyRetry(fn func() error) error {
	var err error
	for range 3 {
		if err = fn(); !isUniqueViolation(err, "agent_image_keys_key_hash_unique") {
			return err
		}
	}
	return err
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}
