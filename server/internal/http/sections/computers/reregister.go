package computers

import (
	"context"

	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	computersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/computers"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Перерегистрация и отключение ПК (Этап 3, G7): кнопки на экране «Подключение ПК», сервис agent/enroll.
// Роли — как для кодов регистрации (enroll.CanRegister: owner, admin, manager, operator).

const msgComputerRevoked = "ПК отключён."

// enrollmentComputers — ПК клуба для выбора на экране (только тем, кто может регистрировать ПК).
func enrollmentComputers(ctx context.Context, q *store.Queries, clubID int64, out *computersapi.Enrollment) error {
	rows, err := q.EnrollmentComputers(ctx, clubID)
	if err != nil {
		return err
	}
	list := make([]computersapi.EnrollmentComputer, 0, len(rows))
	for _, c := range rows {
		list = append(list, computersapi.EnrollmentComputer{Id: c.ID, ZoneId: c.ClubZoneID, Name: clubmap.DisplayName(c.DisplayName, c.DeviceName, c.Number)})
	}
	out.Computers = &list
	return nil
}

// ReregisterComputer — «Перерегистрировать»: сертификаты карточки отозваны, код — для этой карточки.
func (h *handlers) ReregisterComputer(ctx context.Context, req computersapi.ReregisterComputerRequestObject) (computersapi.ReregisterComputerResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	c, err := svc.Reregister(ctx, p, req.ComputerId, clientIP(ctx))
	if err != nil {
		return nil, err
	}
	return computersapi.ReregisterComputer201JSONResponse{Id: c.ID, Code: c.Code, ExpiresAt: c.ExpiresAt}, nil
}

// RevokeComputer — «Отключить»: все сертификаты карточки отозваны.
func (h *handlers) RevokeComputer(ctx context.Context, req computersapi.RevokeComputerRequestObject) (computersapi.RevokeComputerResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	if _, err := svc.RevokeComputer(ctx, p, req.ComputerId, clientIP(ctx)); err != nil {
		return nil, err
	}
	return computersapi.RevokeComputer200JSONResponse{Message: h.d.T(ctx, msgComputerRevoked, nil)}, nil
}
