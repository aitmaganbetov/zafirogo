package enroll

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// EnrollInput — запрос регистрации ПК.
type EnrollInput struct {
	Code string
	// ImageKey — ключ образа бездискового ПК (вместо Code), решение Q-037.
	ImageKey string
	CSRPEM   string
	Device   *agentv1.DeviceInfo
	// IP — адрес ПК (лимиты, аудит, club_computers.remote_ip).
	IP netip.Addr
}

// card — карточка ПК (club_computers) в объёме, нужном регистрации.
type card struct {
	ID          int64
	ClubZoneID  int64
	Number      int64
	DisplayName *string
	DeviceName  *string
	DeviceID    *string
	MacAddress  *string
}

// device — проверенные сведения о ПК.
type device struct {
	uuid, mac, name string
	info            *agentv1.DeviceInfo
}

// Enroll регистрирует ПК по одноразовому коду: код гасится атомарно, карточка находится или
// создаётся (ClubComputerRegistrar), прежние сертификаты карточки отзываются, выпускается новый.
func (s *Service) Enroll(ctx context.Context, in EnrollInput) (*agentv1.EnrollResponse, error) {
	ip := ""
	if in.IP.IsValid() {
		ip = in.IP.String()
	}
	failKey := "agent-enroll-fail:" + ip
	if blocked, retry, err := s.opts.Limiter.Exceeded(ctx, failKey, enrollFailuresIP, limitWindow); err != nil {
		return nil, err
	} else if blocked {
		return nil, rateLimited(retry)
	}
	if ok, retry, err := s.opts.Limiter.Hit(ctx, "agent-enroll-ip:"+ip, enrollPerIP, limitWindow); err != nil {
		return nil, err
	} else if !ok {
		return nil, rateLimited(retry)
	}
	dev, err := checkDevice(in.Device)
	if err != nil {
		return nil, err
	}
	if ok, retry, err := s.opts.Limiter.Hit(ctx, "agent-enroll:"+ip+"|"+deviceKey(dev.uuid), enrollPerDevice, limitWindow); err != nil {
		return nil, err
	} else if !ok {
		return nil, rateLimited(retry)
	}

	if in.ImageKey != "" {
		if in.Code != "" {
			return nil, ErrTwoSecrets
		}
		return s.enrollByImageKey(ctx, in, dev, failKey)
	}

	code := NormalizeCode(in.Code)
	if code == "" {
		return nil, s.codeFailure(ctx, failKey, ErrCodeInvalid)
	}
	csr, err := pki.ParseCSR([]byte(in.CSRPEM))
	if err != nil {
		return nil, ErrCSRInvalid
	}

	var (
		resp    *agentv1.EnrollResponse
		issued  pki.Issued
		clubID  int64
		pc      card
		revoked []string
	)
	err = s.lookupTx(ctx, hashCode(s.opts.CodePepper, code), lookupCode, func(q *store.Queries, codeID, club int64) error {
		clubID = club
		consumed, err := q.ConsumeEnrollmentCode(ctx, store.ConsumeEnrollmentCodeParams{
			UsedIp: addrPtr(in.IP), ClubID: club, ID: codeID})
		if err != nil {
			return notFound(err, ErrCodeExpired)
		}
		clubRow, err := q.LockClubForEnroll(ctx, club)
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

		var created bool
		pc, created, err = s.findOrRegister(ctx, q, club, consumed, dev, sub.Features)
		if err != nil {
			return err
		}
		if err := s.updateHardware(ctx, q, club, pc.ID, dev, in.IP); err != nil {
			return err
		}
		// Повторный Enroll той же карточки отзывает прежний сертификат (как issueAgentToken отзывал токен).
		if revoked, err = q.RevokeComputerCertificates(ctx, store.RevokeComputerCertificatesParams{
			Reason: "reenrolled", ClubID: club, ClubComputerID: pc.ID}); err != nil {
			return err
		}
		if issued, err = s.opts.CA.Issue(pki.IssueRequest{CSR: csr, ClubID: club, ComputerID: pc.ID,
			Lifetime: s.opts.CertLifetime}); err != nil {
			return err
		}
		if err := q.InsertAgentCertificate(ctx, store.InsertAgentCertificateParams{
			Serial: issued.Serial, ClubID: club, ClubComputerID: pc.ID, SpkiSha256: issued.SPKISHA256,
			NotBefore: issued.NotBefore, NotAfter: issued.NotAfter, IssuedVia: "enroll", EnrollmentCodeID: &codeID,
			MacAddress: ptr(dev.mac), HardwareUuid: ptr(dev.uuid),
		}); err != nil {
			return err
		}
		if err := q.SetEnrollmentCodeComputer(ctx, store.SetEnrollmentCodeComputerParams{
			ComputerID: &pc.ID, ClubID: club, ID: codeID}); err != nil {
			return err
		}
		if err := s.audit(ctx, q, club, consumed.CreatedByUserID, "agent", "agent.enrolled", &pc.ID, in.IP, map[string]any{
			"serial": issued.Serial, "not_after": issued.NotAfter, "code_id": codeID, "computer_created": created,
			"revoked": revoked, "mac_address": dev.mac, "hardware_uuid": dev.uuid,
		}); err != nil {
			return err
		}
		resp = &agentv1.EnrollResponse{
			CertificatePem: issued.CertPEM, CaChainPem: s.opts.CA.ChainPEM(), ComputerId: pc.ID, ClubId: club,
			ClubName: clubRow.Name, ComputerNumber: strconv.FormatInt(pc.Number, 10),
			NotAfter: timestamppb.New(issued.NotAfter), RenewAfter: timestamppb.New(issued.RenewAfter),
			AgentEndpoint: s.opts.AgentEndpoint, ComputerCreated: created,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrCodeInvalid) || errors.Is(err, ErrCodeExpired) {
			return nil, s.codeFailure(ctx, failKey, err)
		}
		return nil, err
	}
	if s.opts.Cache != nil {
		s.opts.Cache.Add(issued.Serial, clubID, pc.ID, issued.NotAfter, false)
	}
	s.afterRevoke(ctx, clubID, pc.ID, revoked)
	s.opts.Logger.InfoContext(ctx, "agent enrolled", "club_id", clubID, "computer_id", pc.ID,
		"serial", issued.Serial, "ip", ip)
	return resp, nil
}

func (s *Service) codeFailure(ctx context.Context, failKey string, err error) error {
	if _, _, lerr := s.opts.Limiter.Hit(ctx, failKey, enrollFailuresIP, limitWindow); lerr != nil {
		s.opts.Logger.WarnContext(ctx, "enroll limiter", "error", lerr)
	}
	return err
}

// lookupFunc ищет секрет (код или ключ образа) по хешу до того, как известен клуб.
type lookupFunc func(ctx context.Context, q *store.Queries, hash []byte) (id, clubID int64, err error)

func lookupCode(ctx context.Context, q *store.Queries, hash []byte) (int64, int64, error) {
	code, err := q.LookupEnrollmentCode(ctx, hash)
	if err != nil {
		return 0, 0, notFound(err, ErrCodeInvalid)
	}
	return code.ID, code.ClubID, nil
}

// lookupTx — транзакция Enroll: сначала секрет ищется через политику enroll_code_lookup / image_key_lookup
// (app.enroll_code_hash), затем ставится app.club_id найденного клуба — дальше обычная тенантная
// транзакция. Повтор при deadlock/serialization failure — как db.InTenantTx.
func (s *Service) lookupTx(ctx context.Context, codeHash []byte, lookup lookupFunc, fn func(q *store.Queries, codeID, clubID int64) error) error {
	var err error
	for attempt := 1; attempt <= db.MaxAttempts; attempt++ {
		err = pgx.BeginTxFunc(ctx, s.opts.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.enroll_code_hash', $1, true), set_config('lock_timeout', $2, true)",
				hex.EncodeToString(codeHash), strconv.FormatInt(db.LockTimeout.Milliseconds(), 10)+"ms"); err != nil {
				return fmt.Errorf("enroll: set code hash: %w", err)
			}
			q := store.New(tx)
			id, clubID, err := lookup(ctx, q, codeHash)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true), set_config('app.enroll_code_hash', '', true)",
				strconv.FormatInt(clubID, 10)); err != nil {
				return fmt.Errorf("enroll: set tenant: %w", err)
			}
			return fn(q, id, clubID)
		})
		if err == nil || !db.Retryable(err) {
			return err
		}
	}
	return err
}

func checkDevice(d *agentv1.DeviceInfo) (device, error) {
	if d == nil {
		return device{}, validation("device", "Сведения о ПК обязательны.")
	}
	uuid := strings.TrimSpace(d.GetHardwareUuid())
	if uuid == "" {
		return device{}, validation("device.hardware_uuid", "Идентификатор ПК обязателен.")
	}
	for field, v := range map[string]string{
		"device.hardware_uuid": uuid, "device.hostname": d.GetHostname(), "device.agent_version": d.GetAgentVersion(),
		"device.os_version": d.GetOsVersion(), "device.cpu_model": d.GetCpuModel(), "device.gpu_model": d.GetGpuModel(),
	} {
		if len([]rune(v)) > maxDeviceFieldLen {
			return device{}, validation(field, "Слишком длинное значение.")
		}
	}
	if len(d.GetMacAddress()) > 32 {
		return device{}, validation("device.mac_address", "Слишком длинное значение.")
	}
	if len(d.GetLocalIp()) > 45 {
		return device{}, validation("device.local_ip", "Слишком длинное значение.")
	}
	if c := d.GetCpuCores(); c < 0 || c > 1024 {
		return device{}, validation("device.cpu_cores", "Неверное число ядер.")
	}
	if d.GetRamTotalMb() < 0 {
		return device{}, validation("device.ram_total_mb", "Неверный объём памяти.")
	}
	// Laravel: AgentEnrollController — device_name пустое → 'PC'.
	name := strings.TrimSpace(d.GetHostname())
	if name == "" {
		name = "PC"
	}
	return device{uuid: uuid, mac: NormalizeMAC(d.GetMacAddress()), name: name, info: d}, nil
}

// findOrRegister — карточка ПК: из кода (перерегистрация) или ClubComputerRegistrar::find(),
// иначе ClubComputerRegistrar::register(). MAC пишется только в пустое поле (adoptMac).
func (s *Service) findOrRegister(ctx context.Context, q *store.Queries, clubID int64, code store.ConsumeEnrollmentCodeRow,
	dev device, features []byte) (card, bool, error) {
	var (
		pc  card
		err error
	)
	if code.ClubComputerID != nil {
		var r store.GetComputerForEnrollRow
		if r, err = q.GetComputerForEnroll(ctx, store.GetComputerForEnrollParams{ClubID: clubID, ID: *code.ClubComputerID}); err != nil {
			return card{}, false, notFound(err, ErrComputerNotFound)
		}
		pc = card(r)
	} else {
		var found bool
		if pc, found, err = findCard(ctx, q, clubID, dev); err != nil {
			return card{}, false, err
		}
		if !found {
			pc, err = s.register(ctx, q, clubID, code, dev, features)
			return pc, err == nil, err
		}
	}
	if dev.mac != "" && pc.MacAddress == nil {
		if err := q.AdoptComputerMac(ctx, store.AdoptComputerMacParams{MacAddress: &dev.mac, ClubID: clubID, ID: pc.ID}); err != nil {
			if isUniqueViolation(err, "club_computers_club_mac_unique") {
				return card{}, false, validation("device.mac_address", "Этот MAC-адрес уже у другого ПК клуба.")
			}
			return card{}, false, err
		}
		pc.MacAddress = &dev.mac
	}
	return pc, false, nil
}

// findCard — ClubComputerRegistrar::find() (стр. 97–115): по MAC; если нет — карточка с этим UUID без MAC;
// без MAC — по UUID, иначе по имени ПК.
func findCard(ctx context.Context, q *store.Queries, clubID int64, dev device) (card, bool, error) {
	var (
		pc  card
		err error
	)
	switch {
	case dev.mac != "":
		var r store.FindComputerByMacRow
		r, err = q.FindComputerByMac(ctx, store.FindComputerByMacParams{ClubID: clubID, MacAddress: &dev.mac})
		pc = card(r)
		if errors.Is(err, pgx.ErrNoRows) && dev.uuid != "" {
			var r2 store.FindComputerByDeviceIDWithoutMacRow
			r2, err = q.FindComputerByDeviceIDWithoutMac(ctx, store.FindComputerByDeviceIDWithoutMacParams{ClubID: clubID, DeviceID: dev.uuid})
			pc = card(r2)
		}
	case dev.uuid != "":
		var r store.FindComputerByDeviceIDRow
		r, err = q.FindComputerByDeviceID(ctx, store.FindComputerByDeviceIDParams{ClubID: clubID, DeviceID: dev.uuid})
		pc = card(r)
	default:
		var r store.FindComputerByDeviceNameRow
		r, err = q.FindComputerByDeviceName(ctx, store.FindComputerByDeviceNameParams{ClubID: clubID, DeviceName: dev.name})
		pc = card(r)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return card{}, false, nil
	}
	return pc, err == nil, err
}

// register — ClubComputerRegistrar::register() (стр. 21–86) под блокировкой клуба (LockClubForEnroll).
func (s *Service) register(ctx context.Context, q *store.Queries, clubID int64, code store.ConsumeEnrollmentCodeRow,
	dev device, features []byte) (card, error) {
	deviceID := dev.uuid
	// Новый MAC — новый ПК, даже если его UUID уже на другой карточке (ПК одного образа):
	// UUID остаётся той карточке, эта известна по MAC.
	if dev.mac != "" && deviceID != "" {
		taken, err := q.DeviceIDTaken(ctx, store.DeviceIDTakenParams{ClubID: clubID, DeviceID: deviceID})
		if err != nil {
			return card{}, err
		}
		if taken {
			deviceID = MACDeviceID(dev.mac)
		}
	}
	if limit, ok := maxComputers(features); ok {
		count, err := q.CountComputersByClub(ctx, clubID)
		if err != nil {
			return card{}, err
		}
		if count >= limit {
			return card{}, ErrComputerLimit
		}
	}
	var (
		zoneID   int64
		capacity *int64
	)
	if code.ClubZoneID != nil {
		z, err := q.GetZoneForEnroll(ctx, store.GetZoneForEnrollParams{ClubID: clubID, ID: *code.ClubZoneID})
		if err != nil {
			return card{}, notFound(err, ErrZoneNotFound)
		}
		zoneID, capacity = z.ID, z.Capacity
	} else {
		z, err := q.GetDefaultZone(ctx, clubID)
		if errors.Is(err, pgx.ErrNoRows) {
			var nz store.CreateDefaultZoneRow
			nz, err = q.CreateDefaultZone(ctx, clubID)
			z = store.GetDefaultZoneRow(nz)
		}
		if err != nil {
			return card{}, err
		}
		zoneID, capacity = z.ID, z.Capacity
	}
	if capacity != nil {
		n, err := q.CountZoneComputers(ctx, store.CountZoneComputersParams{ClubID: clubID, ZoneID: zoneID})
		if err != nil {
			return card{}, err
		}
		if n >= *capacity {
			return card{}, ErrZoneFull
		}
	}
	maxNumber, err := q.MaxZoneComputerNumber(ctx, store.MaxZoneComputerNumberParams{ClubID: clubID, ZoneID: zoneID})
	if err != nil {
		return card{}, err
	}
	r, err := q.InsertEnrolledComputer(ctx, store.InsertEnrolledComputerParams{
		ClubID: clubID, ClubZoneID: zoneID, Number: maxNumber + 1, DeviceName: &dev.name,
		DeviceID: ptr(deviceID), MacAddress: ptr(dev.mac),
	})
	if isUniqueViolation(err, "") {
		// Laravel ловит дубликат и ищет карточку снова; здесь клуб заблокирован FOR UPDATE, поэтому
		// дубликат возможен только с карточкой, добавленной в обход регистрации.
		return card{}, ErrDeviceTaken
	}
	if err != nil {
		return card{}, err
	}
	pc := card(r)
	if s.opts.Activity != nil {
		// Laravel: ClubComputerRegistrar::register() стр. 64–69, событие computer.registered.
		var creator int64
		if code.CreatedByUserID != nil {
			creator = *code.CreatedByUserID
		}
		zoneName := ""
		if z, err := q.GetZoneForEnroll(ctx, store.GetZoneForEnrollParams{ClubID: clubID, ID: zoneID}); err == nil {
			zoneName = z.Name
		}
		name := displayName(pc.DisplayName, pc.DeviceName, pc.Number)
		if err := s.opts.Activity(ctx, q, ActivityEvent{
			ClubID: clubID, UserID: creator, Event: "computer.registered",
			Description: "Агент зарегистрировал ПК :computer в зоне :zone",
			Params:      map[string]string{"computer": name, "zone": zoneName},
			SubjectType: "club_computer", SubjectID: pc.ID,
			Details: map[string]string{"device_name": dev.name, "device_id": orDash(pc.DeviceID),
				"mac_address": orDash(pc.MacAddress), "zone": zoneName},
		}); err != nil {
			return card{}, err
		}
	}
	return pc, nil
}

func (s *Service) updateHardware(ctx context.Context, q *store.Queries, clubID, computerID int64, dev device, ip netip.Addr) error {
	d := dev.info
	var cores *int32
	if c := d.GetCpuCores(); c > 0 {
		cores = &c
	}
	var remote *string
	if ip.IsValid() {
		r := ip.String()
		remote = &r
	}
	return q.UpdateComputerHardware(ctx, store.UpdateComputerHardwareParams{
		AgentVersion: ptr(truncate(d.GetAgentVersion(), 50)), OsVersion: ptr(d.GetOsVersion()), CpuModel: ptr(d.GetCpuModel()),
		CpuCores: cores, RamTotalMb: ptr(d.GetRamTotalMb()), GpuModel: ptr(d.GetGpuModel()), LocalIp: ptr(d.GetLocalIp()),
		RemoteIp: remote, ClubID: clubID, ID: computerID,
	})
}

// ---------------------------------------------------------------------------
// Продление
// ---------------------------------------------------------------------------

// RenewInput — запрос продления (под mTLS).
type RenewInput struct {
	CSRPEM string
	Device *agentv1.DeviceInfo
	// Current — текущий клиентский сертификат соединения.
	Current *x509.Certificate
	IP      netip.Addr
}

// RenewCertificate выпускает новый сертификат на новую пару ключей. Прежний действует до первого
// использования нового (тогда отзывается как superseded) или до своего not_after.
// Подписка клуба не проверяется (enrollment.proto, Q-039): ПК не теряет регистрацию.
func (s *Service) RenewCertificate(ctx context.Context, id identity.Identity, in RenewInput) (*agentv1.RenewCertificateResponse, error) {
	if id.ClubID <= 0 || id.ComputerID <= 0 || in.Current == nil {
		return nil, ErrNoIdentity
	}
	if ok, retry, err := s.opts.Limiter.Hit(ctx, "agent-renew:"+strconv.FormatInt(id.ComputerID, 10), renewPerComputer, limitWindow); err != nil {
		return nil, err
	} else if !ok {
		return nil, rateLimited(retry)
	}
	dev, err := checkDevice(in.Device)
	if err != nil {
		return nil, err
	}
	csr, err := pki.ParseCSR([]byte(in.CSRPEM))
	if err != nil || pki.SamePublicKey(csr, in.Current) {
		// Продление — только с новой парой ключей.
		return nil, ErrCSRInvalid
	}
	var (
		issued     pki.Issued
		superseded []string
		prevAfter  time.Time
	)
	err = db.InTenantTx(ctx, s.opts.Pool, id.ClubID, func(q *store.Queries) error {
		cur, err := q.GetAgentCertificate(ctx, store.GetAgentCertificateParams{ClubID: id.ClubID, Serial: id.CertSerial})
		if err != nil {
			return notFound(err, ErrCertificateRevoked)
		}
		if cur.RevokedAt != nil || cur.ClubComputerID != id.ComputerID {
			return ErrCertificateRevoked
		}
		prevAfter = cur.NotAfter
		// Решение Q-037 (docs/DECISIONS.md): сертификат бездискового ПК продлевается тоже на 24 часа и только
		// пока ключ образа, по которому он выдан, действует (отзыв ключа = ПК клуба теряют связь за ≤ 24 ч).
		lifetime := s.opts.CertLifetime
		if cur.Diskless {
			lifetime = s.opts.DisklessCertLifetime
			if err := s.checkImageKey(ctx, q, id.ClubID, cur.ImageKeyID, in.IP); err != nil {
				return err
			}
		}
		pc, err := q.GetComputerForEnroll(ctx, store.GetComputerForEnrollParams{ClubID: id.ClubID, ID: id.ComputerID})
		if err != nil {
			return notFound(err, ErrComputerNotFound)
		}
		if !samePC(pc.DeviceID, pc.MacAddress, dev.uuid, dev.mac) {
			return ErrDeviceMismatch
		}
		// Остаются действующими только текущий и новый сертификаты (прежние незавершённые продления — прочь).
		if superseded, err = q.RevokeComputerCertificates(ctx, store.RevokeComputerCertificatesParams{
			Reason: "superseded", ClubID: id.ClubID, ClubComputerID: id.ComputerID, ExceptSerial: &id.CertSerial}); err != nil {
			return err
		}
		if issued, err = s.opts.CA.Issue(pki.IssueRequest{CSR: csr, ClubID: id.ClubID, ComputerID: id.ComputerID,
			Lifetime: lifetime}); err != nil {
			return err
		}
		if err := q.InsertAgentCertificate(ctx, store.InsertAgentCertificateParams{
			Serial: issued.Serial, ClubID: id.ClubID, ClubComputerID: id.ComputerID, SpkiSha256: issued.SPKISHA256,
			NotBefore: issued.NotBefore, NotAfter: issued.NotAfter, IssuedVia: "renew", ReplacesSerial: &id.CertSerial,
			MacAddress: ptr(dev.mac), HardwareUuid: ptr(dev.uuid), Diskless: cur.Diskless, ImageKeyID: cur.ImageKeyID,
		}); err != nil {
			return err
		}
		return s.audit(ctx, q, id.ClubID, nil, "agent", "agent.certificate.renewed", &id.ComputerID, in.IP, map[string]any{
			"serial": issued.Serial, "replaces": id.CertSerial, "not_after": issued.NotAfter, "superseded": superseded,
			"diskless": cur.Diskless,
		})
	})
	if err != nil {
		return nil, err
	}
	if s.opts.Cache != nil {
		s.opts.Cache.Add(issued.Serial, id.ClubID, id.ComputerID, issued.NotAfter, true)
	}
	s.afterRevoke(ctx, id.ClubID, id.ComputerID, superseded)
	prevValid := prevAfter.Sub(s.opts.Now())
	if prevValid < 0 {
		prevValid = 0
	}
	return &agentv1.RenewCertificateResponse{
		CertificatePem: issued.CertPEM, CaChainPem: s.opts.CA.ChainPEM(),
		NotAfter: timestamppb.New(issued.NotAfter), RenewAfter: timestamppb.New(issued.RenewAfter),
		PreviousValidFor: durationpb.New(prevValid),
	}, nil
}

// markFirstUse — первое подключение с продлённым сертификатом: прежний отзывается (superseded).
func (s *Service) markFirstUse(ctx context.Context, clubID int64, serial string) error {
	var (
		replaced   *string
		computerID int64
	)
	err := db.InTenantTx(ctx, s.opts.Pool, clubID, func(q *store.Queries) error {
		row, err := q.MarkAgentCertificateUsed(ctx, store.MarkAgentCertificateUsedParams{ClubID: clubID, Serial: serial})
		if errors.Is(err, pgx.ErrNoRows) {
			replaced = nil
			return nil
		}
		if err != nil {
			return err
		}
		replaced, computerID = row.ReplacesSerial, row.ClubComputerID
		if replaced == nil {
			return nil
		}
		_, err = q.RevokeAgentCertificate(ctx, store.RevokeAgentCertificateParams{
			Reason: "superseded", ClubID: clubID, Serial: *replaced})
		return err
	})
	if err != nil {
		s.opts.Logger.WarnContext(ctx, "mark agent certificate used", "serial", serial, "error", err)
		return err
	}
	if replaced != nil {
		s.afterRevoke(ctx, clubID, computerID, []string{*replaced})
	}
	return nil
}

func addrPtr(a netip.Addr) *netip.Addr {
	if !a.IsValid() {
		return nil
	}
	return &a
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return *s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
