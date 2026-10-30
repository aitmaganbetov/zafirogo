// Package commands — команды сервера агенту ПК (Command в стриме AgentService.Connect).
//
// Два вида (agent.proto, шапка п. 6):
//   - долговременные (seq > 0): хранятся в Postgres (agent_commands) до expires_at и досылаются после
//     reconnect по Hello.last_command_seq; seq выдаётся счётчиком на ПК (agent_command_counters) в той же
//     транзакции, что и вставка, поэтому порядок коммитов совпадает с порядком seq;
//   - мгновенные (seq = 0): не хранятся; BalanceChanged и GuestCountersChanged — «заменяемые последним»:
//     последнее значение лежит в Valkey с коротким TTL и отправляется после Welcome, если ПК был не в сети.
//
// Почему Postgres, а не Valkey stream/список: долговременные команды редки (питание, сессия, сообщение —
// единицы в час на ПК), их порождает изменение данных в Postgres (StartSession после покупки), им нужна
// надёжность при перезапуске/вытеснении Valkey и итог выполнения для панели. Высокочастотное (статус,
// присутствие) — в Valkey, редкое и важное — в Postgres. docs/agent-protocol.md «Маршрутизация команд».
//
// Доставка: отправитель смотрит владельца стрима ПК в Valkey (presence.Store.OwnerOf) и публикует
// Envelope в NATS на subject инстанса "<prefix>.inst.<instance>" — одна подписка на инстанс вместо
// подписки на каждый ПК (нет лавины SUB/UNSUB при массовом reconnect 50 000 ПК). Если ПК не в сети —
// долговременная команда ждёт в Postgres, мгновенная теряется (её состояние агент получит снимком).
package commands

import (
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Kind — вид команды (agent_commands.kind), имя поля oneof Command.kind.
type Kind string

// Виды команд.
const (
	KindStartSession         Kind = "start_session"
	KindEndSession           Kind = "end_session"
	KindLock                 Kind = "lock"
	KindUnlock               Kind = "unlock"
	KindReboot               Kind = "reboot"
	KindShutdown             Kind = "shutdown"
	KindShowMessage          Kind = "show_message"
	KindBalanceChanged       Kind = "balance_changed"
	KindUpdateAvailable      Kind = "update_available"
	KindTranslationsChanged  Kind = "translations_changed"
	KindConfigChanged        Kind = "config_changed"
	KindPing                 Kind = "ping"
	KindEnterServiceMode     Kind = "enter_service_mode"
	KindWakeOnLan            Kind = "wake_on_lan"
	KindSignOutGuest         Kind = "sign_out_guest"
	KindGuestCountersChanged Kind = "guest_counters_changed"
	KindChatMessageReceived  Kind = "chat_message_received"
	KindQrLoginApproved      Kind = "qr_login_approved"
	KindDrain                Kind = "drain"
)

// KindOf — вид команды; "" — oneof не задан.
func KindOf(c *agentv1.Command) Kind {
	switch c.GetKind().(type) {
	case *agentv1.Command_StartSession:
		return KindStartSession
	case *agentv1.Command_EndSession:
		return KindEndSession
	case *agentv1.Command_Lock:
		return KindLock
	case *agentv1.Command_Unlock:
		return KindUnlock
	case *agentv1.Command_Reboot:
		return KindReboot
	case *agentv1.Command_Shutdown:
		return KindShutdown
	case *agentv1.Command_ShowMessage:
		return KindShowMessage
	case *agentv1.Command_BalanceChanged:
		return KindBalanceChanged
	case *agentv1.Command_UpdateAvailable:
		return KindUpdateAvailable
	case *agentv1.Command_TranslationsChanged:
		return KindTranslationsChanged
	case *agentv1.Command_ConfigChanged:
		return KindConfigChanged
	case *agentv1.Command_Ping:
		return KindPing
	case *agentv1.Command_EnterServiceMode:
		return KindEnterServiceMode
	case *agentv1.Command_WakeOnLan:
		return KindWakeOnLan
	case *agentv1.Command_SignOutGuest:
		return KindSignOutGuest
	case *agentv1.Command_GuestCountersChanged:
		return KindGuestCountersChanged
	case *agentv1.Command_ChatMessageReceived:
		return KindChatMessageReceived
	case *agentv1.Command_QrLoginApproved:
		return KindQrLoginApproved
	case *agentv1.Command_Drain:
		return KindDrain
	default:
		return ""
	}
}

// Старые сроки (Laravel: app/Services/ComputerPower.php).
const (
	// PowerTTL — команды питания и Wake-on-LAN: ComputerPower::COMMAND_TTL_SECONDS = 120.
	PowerTTL = 120 * time.Second
	// MessageTTL — сообщение сотрудника: ComputerPower::MESSAGE_TTL_SECONDS = 300.
	MessageTTL = 300 * time.Second
)

// Policy — как хранить и доставлять команду вида.
type Policy struct {
	// Durable — seq > 0, хранится в Postgres до expires_at.
	Durable bool
	// TTL — срок по умолчанию (если отправитель не задал expires_at). 0 — без срока.
	TTL time.Duration
	// UntilSessionEnd — срок = конец сессии (StartSession: после конца сессии команда бессмысленна).
	UntilSessionEnd bool
	// Coalesce — мгновенная, «заменяемая последним»: последнее значение ждёт ПК в Valkey TTL.
	Coalesce bool
	// Requires — какая возможность агента нужна (nil — любая версия). Без неё команда не отправляется
	// и получает FAILED + COMMAND_UNSUPPORTED (как ComputerPower::carriesOut() по версии агента).
	Requires func(presence.Capabilities) bool
}

// PolicyFor — правила вида. Неизвестный вид — мгновенная без хранения.
func PolicyFor(k Kind) Policy {
	switch k {
	case KindStartSession:
		return Policy{Durable: true, TTL: 12 * time.Hour, UntilSessionEnd: true}
	case KindEndSession, KindLock, KindUnlock, KindSignOutGuest:
		// Нового в Laravel нет (сессия приходила в каждом heartbeat); срок — чтобы ПК, включённый через
		// час, не выполнил устаревшее действие. Актуальную сессию он всё равно получит в Welcome.
		return Policy{Durable: true, TTL: 10 * time.Minute}
	case KindReboot, KindShutdown:
		return Policy{Durable: true, TTL: PowerTTL, Requires: func(c presence.Capabilities) bool { return c.PowerControl }}
	case KindEnterServiceMode:
		return Policy{Durable: true, TTL: PowerTTL, Requires: func(c presence.Capabilities) bool { return c.ServiceMode }}
	case KindWakeOnLan:
		return Policy{Durable: true, TTL: PowerTTL, Requires: func(c presence.Capabilities) bool { return c.WakeOnLan }}
	case KindShowMessage:
		return Policy{Durable: true, TTL: MessageTTL, Requires: func(c presence.Capabilities) bool { return c.ShowMessage }}
	case KindUpdateAvailable:
		return Policy{Durable: true, TTL: 24 * time.Hour, Requires: func(c presence.Capabilities) bool { return c.SelfUpdate }}
	case KindBalanceChanged, KindGuestCountersChanged:
		return Policy{Coalesce: true, TTL: 5 * time.Minute}
	default:
		return Policy{}
	}
}

// CoalescedKinds — виды, последнее значение которых ждёт ПК в Valkey.
var CoalescedKinds = []Kind{KindBalanceChanged, KindGuestCountersChanged}

// Supported — может ли агент с такими возможностями выполнить команду.
func Supported(k Kind, caps presence.Capabilities) bool {
	p := PolicyFor(k)
	return p.Requires == nil || p.Requires(caps)
}
