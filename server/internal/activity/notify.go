package activity

import (
	"context"
	"fmt"

	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// BellEvents — события журнала, которые показывает колокольчик уведомлений, с иконкой Font Awesome.
// Laravel: app/Services/ClubNotifications.php ACTIVITY_EVENTS.
var BellEvents = map[string]string{
	"auth.login_failed":     "fa-user-lock",
	"auth.api_login_failed": "fa-user-lock",
	"agent.exit_pin_failed": "fa-lock",
	"computer.controlled":   "fa-power-off",
	"computer.registered":   "fa-desktop",
	"settings.updated":      "fa-sliders",
	"zone.deleted":          "fa-trash",
	"invitation.accepted":   "fa-user-plus",
}

// notifyBell — запись из списка колокольчика: событие notifications.changed в той же транзакции (вместо опроса
// GET /notifications раз в 60 с; docs/realtime.md).
func notifyBell(ctx context.Context, q *store.Queries, e Entry) error {
	if _, ok := BellEvents[e.Event]; !ok {
		return nil
	}
	if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: e.ClubID, Type: "notifications.changed", Data: map[string]any{}}); err != nil {
		return fmt.Errorf("activity: notify %s: %w", e.Event, err)
	}
	return nil
}
