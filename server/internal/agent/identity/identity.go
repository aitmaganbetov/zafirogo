// Package identity — идентичность агента (ПК клуба), установленная по
// клиентскому сертификату mTLS. Кладётся в контекст gRPC-перехватчиком
// (server/internal/grpc) и читается сервисами агента.
package identity

import "context"

// Identity — кто вызывает: ПК клуба. Берётся только из проверенного
// сертификата (CN/SAN `computer:<id>`, club_id — из таблицы выпущенных
// сертификатов), никогда из полей запроса.
type Identity struct {
	ClubID     int64
	ComputerID int64
	// CertSerial — серийный номер сертификата в hex (нижний регистр, без
	// ведущих нулей), как хранится в agent_certificates.serial.
	CertSerial string
}

type ctxKey struct{}

// WithIdentity возвращает контекст с идентичностью агента.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext достаёт идентичность агента; ok=false — вызов без сертификата
// (например, EnrollmentService.Enroll).
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}
