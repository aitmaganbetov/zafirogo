// Package presence — присутствие ПК клуба: открыт ли стрим агента, его версия, состояние, сессия и загрузка.
//
// Где что хранится (docs/agent-protocol.md, «Heartbeat и запись в БД»):
//   - в памяти инстанса, который держит стрим ПК (server/internal/agent/stream);
//   - в Valkey (Store): владелец стрима (id инстанса и поколение соединения) с TTL и снимок State по ПК —
//     для карты клуба, WebSocket админки и маршрутизации команд с любого инстанса;
//   - в Postgres (Recorder) — только ИЗМЕНЕНИЯ: онлайн/офлайн, agent_state (с дебаунсом), версия агента,
//     IP, железо, загрузка Windows. Status без изменений в Postgres не пишется.
//
// Сравнение со старым (Laravel): app/Services/ClubComputerSessions.php heartbeat()/needsWrite() писал
// club_computers в транзакции с SELECT … FOR UPDATE, если что-то изменилось ИЛИ last_seen_at старше
// SEEN_WRITE_SECONDS = 20 с. Heartbeat шёл раз в 30 с (> 20 с), поэтому почти каждый heartbeat писал в MySQL:
// ≈ 2 записи в минуту на ПК (≈ 1 700 UPDATE/с на 50 000 ПК). Онлайн определялся по last_seen_at моложе 90 с
// (ClubComputer::HEARTBEAT_TIMEOUT_SECONDS). Теперь онлайн = открытый стрим (status = 'online'), last_seen_at
// пишется только при смене состояния, а «зависшие» ПК после падения инстанса находит Sweeper по TTL в Valkey.
package presence

import (
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// Состояния агента — значения club_computers.agent_state (ClubComputer::AGENT_STATES + 'service', 0010).
const (
	StateIdle         = "idle"
	StateLocked       = "locked"
	StateInSession    = "in_session"
	StateShuttingDown = "shutting_down"
	StateService      = "service"
)

// StateName переводит ComputerState протокола в значение agent_state; "" — не задано/неизвестно.
func StateName(s agentv1.ComputerState) string {
	switch s {
	case agentv1.ComputerState_COMPUTER_STATE_IDLE:
		return StateIdle
	case agentv1.ComputerState_COMPUTER_STATE_LOCKED:
		return StateLocked
	case agentv1.ComputerState_COMPUTER_STATE_IN_SESSION:
		return StateInSession
	case agentv1.ComputerState_COMPUTER_STATE_SHUTTING_DOWN:
		return StateShuttingDown
	case agentv1.ComputerState_COMPUTER_STATE_SERVICE:
		return StateService
	default:
		return ""
	}
}

// Capabilities — что умеет агент (Hello.capabilities).
type Capabilities struct {
	SelfUpdate   bool `json:"self_update,omitempty"`
	WakeOnLan    bool `json:"wake_on_lan,omitempty"`
	PowerControl bool `json:"power_control,omitempty"`
	ShowMessage  bool `json:"show_message,omitempty"`
	ServiceMode  bool `json:"service_mode,omitempty"`
}

// CapabilitiesFrom читает возможности из Hello.
func CapabilitiesFrom(c *agentv1.AgentCapabilities) Capabilities {
	return Capabilities{
		SelfUpdate:   c.GetSelfUpdate(),
		WakeOnLan:    c.GetWakeOnLan(),
		PowerControl: c.GetPowerControl(),
		ShowMessage:  c.GetShowMessage(),
		ServiceMode:  c.GetServiceMode(),
	}
}

// Load — загрузка ПК (ComputerLoad): проценты × 100, без float.
type Load struct {
	CPUBasisPoints uint32 `json:"cpu_bp"`
	RAMBasisPoints uint32 `json:"ram_bp"`
	IdleSeconds    uint32 `json:"idle_s"`
	AgentMemoryMB  uint32 `json:"agent_mb"`
}

// LoadFrom читает загрузку из Status.
func LoadFrom(l *agentv1.ComputerLoad) Load {
	return Load{
		CPUBasisPoints: l.GetCpuBasisPoints(),
		RAMBasisPoints: l.GetRamBasisPoints(),
		IdleSeconds:    l.GetIdleSeconds(),
		AgentMemoryMB:  l.GetAgentMemoryMb(),
	}
}

// State — живое состояние ПК (снимок в Valkey). Online = false — стрим закрыт, но офлайн ещё не записан
// (ожидание переподключения, Options.OfflineGrace).
type State struct {
	ClubID          int64        `json:"club_id"`
	ComputerID      int64        `json:"computer_id"`
	Online          bool         `json:"online"`
	Instance        string       `json:"instance"`
	Gen             int64        `json:"gen"`
	ProtocolVersion uint32       `json:"protocol_version"`
	AgentVersion    string       `json:"agent_version,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
	// AgentState — значение agent_state ("" — агент ещё не сообщил).
	AgentState     string `json:"agent_state,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	SignedInUserID int64  `json:"signed_in_user_id,omitempty"`
	Load           Load   `json:"load"`
	// HasLoad — агент прислал ComputerLoad (иначе Load — нули, а не «простаивает»).
	HasLoad bool `json:"load_ok,omitempty"`
	// Health — последнее здоровье ПК (Status.health; nil — агент его не присылал: старый агент или ещё не измерил).
	Health *Health `json:"health,omitempty"`
	// Problems — подтверждённые проблемы здоровья (agent/health: пороги и защита от дребезга), по возрастанию.
	Problems []string `json:"problems,omitempty"`
	// RTTMillis — время ответа агента на Ping по стриму, мс (0 — не измерено).
	RTTMillis   uint32    `json:"rtt_ms,omitempty"`
	RemoteIP    string    `json:"remote_ip,omitempty"`
	LocalIP     string    `json:"local_ip,omitempty"`
	ConnectedAt time.Time `json:"connected_at"`
	// LastSeenAt — последний Status (или подключение / закрытие стрима).
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Significant сообщает, что состояние отличается не только загрузкой и временем (для уведомлений админки).
func (s State) Significant(prev State) bool {
	return s.Online != prev.Online || s.AgentState != prev.AgentState || s.SessionID != prev.SessionID ||
		s.SignedInUserID != prev.SignedInUserID || s.AgentVersion != prev.AgentVersion || s.Gen != prev.Gen
}
