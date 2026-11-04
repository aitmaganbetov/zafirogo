package stream

import (
	"context"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// EventHandler получает события агента (AgentEvent) после проверки и дедупликации по event_id.
// Ошибка — событие не подтверждается (EventAck не уходит), агент повторит его после reconnect.
// Реализацию (завершение сессии с ПК, вход/выход гостя в журнал, ошибки агента в панель) связывает оркестратор.
type EventHandler interface {
	HandleEvent(ctx context.Context, id identity.Identity, ev *agentv1.AgentEvent) error
	// FreshBoot — новый Hello.boot_id: Windows загрузилась заново. Старое поведение — сбросить гостевую
	// сессию с кассы (Laravel: app/Services/ClubComputerSessions.php resetGuestSession(), запись журнала
	// computer.guest_session_reset). Вызывается до Welcome, чтобы снимок сессии был уже после сброса.
	FreshBoot(ctx context.Context, id identity.Identity) error
}

// WelcomeProvider дополняет Welcome данными других модулей: ClubConfig (GuestFeatures, языки, after_session,
// автобиллинг), translations_version, certificate_renew_after. Базовые поля (ПК, зона, сессия, seq, версии
// протокола, server_time, status_interval) Service заполняет сам.
type WelcomeProvider interface {
	CompleteWelcome(ctx context.Context, id identity.Identity, w *agentv1.Welcome) error
}

// ResultHandler — итог команды от агента (для панели: «ПК перезагружается», «сообщение показано»).
// kind — вид команды, если она известна этому инстансу (иначе "").
type ResultHandler interface {
	CommandResult(ctx context.Context, id identity.Identity, kind commands.Kind, res *agentv1.CommandResult)
}

// PresenceListener — изменения присутствия на этом инстансе (подключение, смена состояния/сессии/гостя,
// закрытие стрима). Офлайн после ожидания переподключения сообщает presence.Sweeper.OnOffline.
type PresenceListener interface {
	PresenceChanged(ctx context.Context, st presence.State)
}
