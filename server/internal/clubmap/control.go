package clubmap

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Действия панели управления ПК (ControlClubComputersRequest).
const (
	ActionPowerOn  = "power_on"
	ActionRestart  = "restart"
	ActionPowerOff = "power_off"
	ActionReserve  = "reserve"
	ActionService  = "service"
)

// Actions — допустимые действия.
var Actions = []string{ActionPowerOn, ActionRestart, ActionPowerOff, ActionReserve, ActionService}

// MaxSelected — computer_ids max:200 (control, end-sessions, notify).
const MaxSelected = 200

// MaxWakeRelays — сколько включённых ПК рассылают Wake-on-LAN. В старом коде пакет слали все включённые ПК
// (каждый в своём heartbeat); с командами по стриму это N команд на клуб — достаточно нескольких ПК
// в той же сети (docs/open-questions/stage3-clubmap.md CM-4).
const MaxWakeRelays = 5

// Минимальные версии старого агента (ComputerPower::AGENT_VERSION и др.) — для текста «обновите до версии»,
// если релиз агента не объявлен.
const (
	PowerAgentVersion   = "0.0.66"
	ServiceAgentVersion = "0.0.72"
	MessageAgentVersion = "0.0.73"
)

// Тексты ClubComputerController::controlMessage(), notify(), endSessions() (ключи перевода).
const (
	MsgControlLog        = "Управление ПК (:action): :names"
	MsgOutdatedAll       = "Агент на этих ПК устарел и не выполнит команду: обновите его до версии :version или новее."
	MsgRestartQueued     = "Перезагрузка: ПК получит команду в течение минуты (:count ПК).|Перезагрузка: ПК получат команду в течение минуты (:count ПК).|Перезагрузка: ПК получат команду в течение минуты (:count ПК)."
	MsgServiceQueued     = "Техрежим: агент на ПК закроется в течение минуты, как по PIN-коду (:count ПК).|Техрежим: агент на ПК закроется в течение минуты, как по PIN-коду (:count ПК).|Техрежим: агент на ПК закроется в течение минуты, как по PIN-коду (:count ПК)."
	MsgShutdownQueued    = "Выключение: ПК получит команду в течение минуты (:count ПК).|Выключение: ПК получат команду в течение минуты (:count ПК).|Выключение: ПК получат команду в течение минуты (:count ПК)."
	MsgOutdatedSome      = "На :count ПК агент устарел (нужна версия :version): они команду не выполнят."
	MsgWakeNoMac         = "Не удалось включить: агент ещё не сообщил MAC-адрес этих ПК."
	MsgWakeOldRelays     = "Включить некому: на включённых ПК клуба старый агент. Сигнал Wake-on-LAN отправляет агент версии :version или новее — обновите агент хотя бы на одном ПК."
	MsgWakeNoRelays      = "Включить некому: в клубе нет включённых ПК с агентом, который отправит сигнал Wake-on-LAN."
	MsgWakeSomeNoMac     = "Сигнал включения отправят другие ПК клуба (Wake-on-LAN): :count ПК. Без MAC-адреса: :missing."
	MsgWakeSent          = "Сигнал включения отправят другие ПК клуба (Wake-on-LAN): :count ПК. В BIOS этих ПК должен быть включён Wake-on-LAN."
	MsgNotifyLog         = "Уведомление на ПК: :names"
	MsgNotifyOutdatedAll = "Агент на этих ПК устарел и не покажет уведомление: обновите его до версии :version или новее."
	MsgNotifyQueued      = "Уведомление появится на экране в течение минуты (:count ПК).|Уведомление появится на экранах в течение минуты (:count ПК).|Уведомление появится на экранах в течение минуты (:count ПК)."
	MsgNotifyOutdated    = "На :count ПК агент устарел (нужна версия :version): там уведомления не будет."
	MsgNoSessions        = "На выбранных ПК нет активной сессии."
	MsgSessionsEnded     = "Сессия завершена на :count ПК. Неиспользованное время вернулось клиенту.|Сессии завершены на :count ПК. Неиспользованное время вернулось клиентам.|Сессии завершены на :count ПК. Неиспользованное время вернулось клиентам."
)

// CommandSender — agent/commands.Sender.Send.
type CommandSender interface {
	Send(ctx context.Context, clubID, computerID int64, cmd *agentv1.Command) (string, error)
}

// Presence — присутствие агентов клуба (*presence.Store).
type Presence interface {
	List(ctx context.Context, clubID int64) ([]presence.State, error)
}

// ValidateIDs — computer_ids: required|array|min:1|max:200, *: integer|distinct (exists — проверяет вызывающий
// по ClubMapComputersByIDs).
func ValidateIDs(ids []int64) error {
	switch {
	case len(ids) == 0:
		return rule("computer_ids", "required", "")
	case len(ids) > MaxSelected:
		return rule("computer_ids", "max", strconv.Itoa(MaxSelected))
	}
	seen := make(map[int64]bool, len(ids))
	for i, id := range ids {
		if seen[id] {
			return rule("computer_ids."+strconv.Itoa(i), "distinct", "")
		}
		seen[id] = true
	}
	return nil
}

// Selected — выбранные ПК клуба. strict — каждый id должен быть ПК клуба (exists, ControlClubComputersRequest);
// иначе чужие id молча отбрасываются (notify, end-sessions: whereKey()).
func Selected(ctx context.Context, q *store.Queries, clubID int64, ids []int64, strict bool) ([]store.ClubMapComputersByIDsRow, error) {
	rows, err := q.ClubMapComputersByIDs(ctx, store.ClubMapComputersByIDsParams{ClubID: clubID, Ids: ids})
	if err != nil {
		return nil, err
	}
	if strict && len(rows) != len(ids) {
		found := make(map[int64]bool, len(rows))
		for _, r := range rows {
			found[r.ID] = true
		}
		for i, id := range ids {
			if !found[id] {
				return nil, rule("computer_ids."+strconv.Itoa(i), "exists", "")
			}
		}
	}
	return rows, nil
}

func names(rows []store.ClubMapComputersByIDsRow) string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = DisplayName(r.DisplayName, r.DeviceName, r.Number)
	}
	return strings.Join(out, ", ")
}

// ControlPlan — что сделано в транзакции и что отправить агентам после коммита.
type ControlPlan struct {
	Action string
	IDs    []int64
	// Status — новый статус ПК (restarting, offline) или "" (power_on, service).
	Status string
	// Reserved — для reserve: новый флаг.
	Reserved bool
	Updated  int64
	// Versions — agent_version выбранных ПК (для «устарел», если ПК не в сети).
	Versions map[int64]*string
	// MACs — MAC выбранных ПК (power_on).
	MACs []string
	// StaffMinutes — техрежим: на сколько минут закрыть агент (clubs.agent_staff_minutes).
	StaffMinutes int32
}

// ControlTx — ClubComputerController::control() в транзакции: статус, техрежим, флаг брони, журнал
// computer.controlled, вебхук computer.controlled / computer.reservation_changed, события computer.state.
func ControlTx(ctx context.Context, q *store.Queries, env Env, rows []store.ClubMapComputersByIDsRow, action string) (ControlPlan, error) {
	now := env.Now
	plan := ControlPlan{Action: action, Versions: map[int64]*string{}}
	for _, r := range rows {
		plan.IDs = append(plan.IDs, r.ID)
		plan.Versions[r.ID] = r.AgentVersion
		if r.MacAddress != nil && *r.MacAddress != "" {
			plan.MACs = append(plan.MACs, *r.MacAddress)
		}
	}
	logAction := action
	switch action {
	case ActionReserve:
		plan.Reserved = slices.ContainsFunc(rows, func(r store.ClubMapComputersByIDsRow) bool { return !r.IsReserved })
		n, err := q.ClubMapSetReserved(ctx, store.ClubMapSetReservedParams{Reserved: plan.Reserved, Now: &now, ClubID: env.ClubID, Ids: plan.IDs})
		if err != nil {
			return plan, err
		}
		plan.Updated = n
		logAction = "unreserve"
		if plan.Reserved {
			logAction = "reserve"
		}
	case ActionRestart, ActionPowerOff:
		plan.Status = "restarting"
		if action == ActionPowerOff {
			plan.Status = "offline"
		}
		if err := q.ClubMapSetStatus(ctx, store.ClubMapSetStatusParams{Status: plan.Status, Now: &now, ClubID: env.ClubID, Ids: plan.IDs}); err != nil {
			return plan, err
		}
		plan.Updated = int64(len(plan.IDs))
	case ActionService:
		// Техрежим, как Ctrl+Alt+P с PIN сотрудника: ПК — визит сотрудника (ClubComputerSessions::startStaffVisit).
		settings, err := q.ClubMapSettings(ctx, env.ClubID)
		if err != nil {
			return plan, err
		}
		plan.StaffMinutes = settings.AgentStaffMinutes
		until := now.Add(time.Duration(settings.AgentStaffMinutes) * time.Minute)
		var staff *int64
		if env.UserID > 0 {
			staff = &env.UserID
		}
		if err := q.ClubMapStartStaffVisit(ctx, store.ClubMapStartStaffVisitParams{StaffUserID: staff, StaffUntil: &until, Now: &now, ClubID: env.ClubID, Ids: plan.IDs}); err != nil {
			return plan, err
		}
		plan.Updated = int64(len(plan.IDs))
	case ActionPowerOn:
		if _, err := q.ClubMapMarkWake(ctx, store.ClubMapMarkWakeParams{Now: &now, ClubID: env.ClubID, Ids: plan.IDs}); err != nil {
			return plan, err
		}
		plan.Updated = int64(len(plan.IDs))
	}
	updated := plan.Updated
	if action != ActionReserve {
		updated = int64(len(plan.IDs))
	}
	all := names(rows)
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "computer.controlled", UserID: env.UserID, At: now,
		Description: env.t(MsgControlLog, map[string]string{"action": logAction, "names": all}),
		Details:     map[string]any{"action": logAction, "computers": all, "updated": updated},
	}); err != nil {
		return plan, err
	}
	var hook outbox.Event
	if action == ActionReserve {
		hook = outbox.Event{ClubID: env.ClubID, Type: "computer.reservation_changed", Data: map[string]any{"computer_ids": plan.IDs, "reserved": plan.Reserved}}
	} else {
		var status any
		if plan.Status != "" {
			status = plan.Status
		}
		hook = outbox.Event{ClubID: env.ClubID, Type: "computer.controlled", Data: map[string]any{"computer_ids": plan.IDs, "action": action, "status": status}}
	}
	if _, err := outbox.Add(ctx, q, hook); err != nil {
		return plan, err
	}
	for _, id := range plan.IDs {
		if err := stateChanged(ctx, q, env.ClubID, id); err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// ControlCounts — итог отправки (как результат ComputerPower::queue()).
type ControlCounts struct {
	Queued, Outdated                     int
	Woken, WithoutMAC, Relays, OldRelays int
}

// supports — может ли ПК выполнить команду вида kind: в сети — по возможностям агента (Hello.capabilities);
// не в сети — команда ждёт его в очереди (срок 2–5 мин), устаревшим считается ПК, агент которого ни разу
// не сообщил версию (как carriesOut(null) старого кода).
func supports(kind commands.Kind, st presence.State, online bool, version *string) bool {
	if online {
		return commands.Supported(kind, st.Capabilities)
	}
	return version != nil && *version != ""
}

// SendControl — команды агентам после коммита (ComputerPower::queue()): restart → Reboot, power_off → Shutdown,
// service → EnterServiceMode(staff_minutes), power_on → WakeOnLan(MAC выбранных) нескольким включённым ПК
// клуба с возможностью wake_on_lan. Ошибка отправки одному ПК не прерывает остальных (команда в очереди —
// дойдёт после reconnect).
func SendControl(ctx context.Context, sender CommandSender, pres Presence, clubID, userID int64, plan ControlPlan, log *slog.Logger) ControlCounts {
	var states []presence.State
	if pres != nil {
		var err error
		if states, err = pres.List(ctx, clubID); err != nil && log != nil {
			log.WarnContext(ctx, "club map control: presence", "err", err)
		}
	}
	online := PresenceByID(states)
	var c ControlCounts
	send := func(pc int64, cmd *agentv1.Command) {
		if sender == nil {
			return
		}
		cmd.IssuedByUserId = userID
		if _, err := sender.Send(ctx, clubID, pc, cmd); err != nil && log != nil {
			log.WarnContext(ctx, "club map control: send", "computer_id", pc, "kind", commands.KindOf(cmd), "err", err)
		}
	}
	if plan.Action == ActionPowerOn {
		c.Woken = len(plan.MACs)
		c.WithoutMAC = len(plan.IDs) - c.Woken
		selected := make(map[int64]bool, len(plan.IDs))
		for _, id := range plan.IDs {
			selected[id] = true
		}
		var relays []int64
		for id, st := range online {
			if selected[id] {
				continue
			}
			if commands.Supported(commands.KindWakeOnLan, st.Capabilities) {
				relays = append(relays, id)
			} else {
				c.OldRelays++
			}
		}
		slices.Sort(relays)
		c.Relays = len(relays)
		if c.Woken == 0 {
			return c
		}
		macs := plan.MACs
		if len(macs) > commands.MaxWakeTargets {
			macs = macs[:commands.MaxWakeTargets]
		}
		for i, id := range relays {
			if i >= MaxWakeRelays {
				break
			}
			send(id, &agentv1.Command{Kind: &agentv1.Command_WakeOnLan{WakeOnLan: &agentv1.WakeOnLan{MacAddresses: macs}}})
		}
		return c
	}
	kind := map[string]commands.Kind{ActionRestart: commands.KindReboot, ActionPowerOff: commands.KindShutdown, ActionService: commands.KindEnterServiceMode}[plan.Action]
	for _, id := range plan.IDs {
		c.Queued++
		st, on := online[id]
		if !supports(kind, st, on, plan.Versions[id]) {
			c.Outdated++
			continue
		}
		var cmd *agentv1.Command
		switch plan.Action {
		case ActionRestart:
			cmd = &agentv1.Command{Kind: &agentv1.Command_Reboot{Reboot: &agentv1.Reboot{}}}
		case ActionPowerOff:
			cmd = &agentv1.Command{Kind: &agentv1.Command_Shutdown{Shutdown: &agentv1.Shutdown{}}}
		case ActionService:
			cmd = &agentv1.Command{Kind: &agentv1.Command_EnterServiceMode{EnterServiceMode: &agentv1.EnterServiceMode{StaffMinutes: uint32(max(plan.StaffMinutes, 0))}}} // #nosec G115 -- неотрицательное (проверено)
		}
		send(id, cmd)
	}
	return c
}

// ControlMessage — ClubComputerController::controlMessage(); version — версия агента для текста «обновите до…»
// (объявленный релиз или минимальная версия старого агента). T — перевод с подстановками (trans_choice по count).
func ControlMessage(action string, c ControlCounts, latest string, t Translator) string {
	if action != ActionPowerOn {
		version := PowerAgentVersion
		if action == ActionService {
			version = ServiceAgentVersion
		}
		if latest != "" {
			version = latest
		}
		ready := c.Queued - c.Outdated
		if ready == 0 {
			return t(MsgOutdatedAll, map[string]string{"version": version})
		}
		key := MsgShutdownQueued
		switch action {
		case ActionRestart:
			key = MsgRestartQueued
		case ActionService:
			key = MsgServiceQueued
		}
		msg := t(key, map[string]string{"count": strconv.Itoa(ready)})
		if c.Outdated > 0 {
			msg += " " + t(MsgOutdatedSome, map[string]string{"count": strconv.Itoa(c.Outdated), "version": version})
		}
		return msg
	}
	switch {
	case c.Woken == 0:
		return t(MsgWakeNoMac, nil)
	case c.Relays == 0 && c.OldRelays > 0:
		version := PowerAgentVersion
		if latest != "" {
			version = latest
		}
		return t(MsgWakeOldRelays, map[string]string{"version": version})
	case c.Relays == 0:
		return t(MsgWakeNoRelays, nil)
	case c.WithoutMAC > 0:
		return t(MsgWakeSomeNoMac, map[string]string{"count": strconv.Itoa(c.Woken), "missing": strconv.Itoa(c.WithoutMAC)})
	default:
		return t(MsgWakeSent, map[string]string{"count": strconv.Itoa(c.Woken)})
	}
}

// ValidateMessage — message: required|string|max:300 (после trim — как в старом, trim после валидации).
func ValidateMessage(msg string) (string, error) {
	switch {
	case msg == "":
		return "", rule("message", "required", "")
	case utf8.RuneCountInString(msg) > commands.MaxMessageRunes:
		return "", rule("message", "max_string", strconv.Itoa(commands.MaxMessageRunes))
	}
	t := strings.TrimSpace(msg)
	if t == "" {
		return "", rule("message", "required", "")
	}
	return t, nil
}

// NotifyTx — журнал computer.messaged (ClubComputerController::notify()).
func NotifyTx(ctx context.Context, q *store.Queries, env Env, rows []store.ClubMapComputersByIDsRow, text string) error {
	all := names(rows)
	_, err := activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "computer.messaged", UserID: env.UserID, At: env.Now,
		Description: env.t(MsgNotifyLog, map[string]string{"names": all}),
		Details:     map[string]any{"computers": all, "message": text},
	})
	return err
}

// SendNotify — ShowMessage выбранным ПК (срок 5 мин, ComputerPower::MESSAGE_TTL_SECONDS); возвращает
// queued и outdated.
func SendNotify(ctx context.Context, sender CommandSender, pres Presence, clubID, userID int64, rows []store.ClubMapComputersByIDsRow, text string, log *slog.Logger) ControlCounts {
	var states []presence.State
	if pres != nil {
		states, _ = pres.List(ctx, clubID)
	}
	online := PresenceByID(states)
	var c ControlCounts
	for _, r := range rows {
		c.Queued++
		st, on := online[r.ID]
		if !supports(commands.KindShowMessage, st, on, r.AgentVersion) {
			c.Outdated++
			continue
		}
		if sender == nil {
			continue
		}
		cmd := &agentv1.Command{IssuedByUserId: userID, Kind: &agentv1.Command_ShowMessage{ShowMessage: &agentv1.ShowMessage{Text: text}}}
		if _, err := sender.Send(ctx, clubID, r.ID, cmd); err != nil && log != nil {
			log.WarnContext(ctx, "club map notify: send", "computer_id", r.ID, "err", err)
		}
	}
	return c
}

// NotifyMessage — текст ответа notify().
func NotifyMessage(c ControlCounts, latest string, t Translator) string {
	version := MessageAgentVersion
	if latest != "" {
		version = latest
	}
	ready := c.Queued - c.Outdated
	if ready == 0 {
		return t(MsgNotifyOutdatedAll, map[string]string{"version": version})
	}
	msg := t(MsgNotifyQueued, map[string]string{"count": strconv.Itoa(ready)})
	if c.Outdated > 0 {
		msg += " " + t(MsgNotifyOutdated, map[string]string{"count": strconv.Itoa(c.Outdated), "version": version})
	}
	return msg
}
