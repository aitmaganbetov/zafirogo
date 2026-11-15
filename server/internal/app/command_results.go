package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/realtime"
)

// CommandResultEvent — тип события панели с итогом команды ПК (право club_map.view, раздел computer).
const CommandResultEvent = "computer.command"

// commandResultData — данные события computer.command.
type commandResultData struct {
	ComputerID int64  `json:"computer_id"`
	CommandID  string `json:"command_id"`
	// Kind — вид команды (reboot, shutdown, lock, unlock, show_message, enter_service_mode, wake_on_lan,
	// update_available); пусто — команду отправлял другой инстанс и вид здесь неизвестен.
	Kind string `json:"kind,omitempty"`
	// Status — succeeded | failed.
	Status string `json:"status"`
	// ErrorCode — причина отказа (имя ErrorCode протокола без префикса, нижним регистром); пусто — нет.
	ErrorCode string `json:"error_code,omitempty"`
}

// panelCommandKinds — команды, которые отдаёт сотрудник из панели: их итог показывается на карте («ПК
// перезагружается», «сообщение показано»). Сессия, баланс, конфигурация и прочие служебные команды панели не нужны.
var panelCommandKinds = map[commands.Kind]bool{
	commands.KindReboot: true, commands.KindShutdown: true, commands.KindLock: true, commands.KindUnlock: true,
	commands.KindShowMessage: true, commands.KindEnterServiceMode: true, commands.KindWakeOnLan: true,
	commands.KindUpdateAvailable: true,
}

// commandResults — stream.ResultHandler (решение W-1, docs/open-questions/stage2-wiring.md): итог команды агента →
// событие computer.command в WebSocket панели. Итог уже записан в agent_commands (commands.Queue.Record), поэтому
// событие идёт мимо outbox и Postgres — core NATS, как computer.load (realtime.LiveSubject): потерянное событие
// панель восполнит перечитыванием ПК. Вызывается из горутины чтения стрима — публикация без ожидания.
type commandResults struct {
	nats *nats.Conn
	now  func() time.Time
	log  *slog.Logger
}

// CommandResult — stream.ResultHandler.
func (c *commandResults) CommandResult(ctx context.Context, id identity.Identity, kind commands.Kind, res *agentv1.CommandResult) {
	data, ok := commandResultOf(id, kind, res)
	if !ok {
		return
	}
	if data.Status == "failed" {
		c.log.InfoContext(ctx, "agent command failed", "club_id", id.ClubID, "computer_id", id.ComputerID,
			"kind", data.Kind, "error_code", data.ErrorCode)
	}
	if c.nats == nil {
		return
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	raw, err := json.Marshal(data)
	if err == nil {
		var b []byte
		b, err = json.Marshal(outbox.Envelope{ID: uuid.NewString(), Type: CommandResultEvent, ClubID: id.ClubID,
			CreatedAt: now().UTC(), Data: raw})
		if err == nil {
			err = c.nats.Publish(realtime.LiveSubject(id.ClubID, CommandResultEvent), b)
		}
	}
	if err != nil {
		c.log.WarnContext(ctx, "computer.command event", "club_id", id.ClubID, "computer_id", id.ComputerID, "err", err)
	}
}

// commandResultOf — событие панели по итогу команды; false — событие не нужно (промежуточный ACCEPTED, повтор,
// служебная команда).
func commandResultOf(id identity.Identity, kind commands.Kind, res *agentv1.CommandResult) (commandResultData, bool) {
	if id.ClubID <= 0 || id.ComputerID <= 0 || res == nil {
		return commandResultData{}, false
	}
	var status string
	switch res.GetStatus() {
	case agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED:
		status = "succeeded"
	case agentv1.CommandStatus_COMMAND_STATUS_FAILED:
		status = "failed"
	default:
		return commandResultData{}, false // ACCEPTED — ещё выполняется; DUPLICATE — итог уже был
	}
	// Вид неизвестен (команду отправил другой инстанс): событие нужно только для долговременных команд — это
	// команды панели и сессии; мгновенные без вида (баланс, счётчики) пропускаются.
	if kind == "" && res.GetSeq() == 0 || kind != "" && !panelCommandKinds[kind] {
		return commandResultData{}, false
	}
	d := commandResultData{ComputerID: id.ComputerID, CommandID: res.GetCommandId(), Kind: string(kind), Status: status}
	if ec := res.GetErrorCode(); ec != agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED {
		d.ErrorCode = strings.ToLower(strings.TrimPrefix(ec.String(), "ERROR_CODE_"))
	}
	return d, true
}
