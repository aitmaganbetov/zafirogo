package stream

import "time"

// Потолок частоты Status на поток (аудит безопасности A-02).
//
// Правило «не чаще 1 Status в 2 с» (agent.proto п. 7, Options.MinStatusGap) отбрасывало только Status без изменений:
// чередуя состояние, гостя или периферию, один ПК заставлял сервер обрабатывать ≈ 1 500 Status в секунду (запись
// снимка в Valkey, outbox в Postgres, события в NATS и WebSocket панели). Здесь — потолок ДО любой обработки:
// ведро на StatusBurst сообщений, пополняется одним за MinStatusGap. Честный агент шлёт Status раз в
// status_interval и сразу при смене состояния, гостя, периферии или SMART — несколько сообщений подряд (вход гостя,
// запуск сессии) укладываются во всплеск. Сверх потолка Status отбрасывается; StatusFloodDrops отброшенных за
// floodWindow — поток закрывается RESOURCE_EXHAUSTED (агент переподключится с обычной задержкой).
const (
	// DefaultStatusBurst — всплеск Status на поток.
	DefaultStatusBurst = 10
	// DefaultStatusFloodDrops — отброшенных Status за floodWindow, после которых поток закрывается.
	DefaultStatusFloodDrops = 300
	floodWindow             = time.Minute
)

// statusLimit — состояние потолка одного потока; читает и пишет только горутина чтения стрима.
type statusLimit struct {
	tokens  float64
	at      time.Time // когда ведро пересчитано; ноль — ведро ещё полное
	dropAt  time.Time // начало окна подсчёта отброшенных
	dropped int
}

// allow списывает один Status; false — потолок исчерпан.
func (l *statusLimit) allow(now time.Time, burst int, every time.Duration) bool {
	if l.at.IsZero() {
		l.tokens = float64(burst)
	} else if d := now.Sub(l.at); d > 0 && every > 0 {
		l.tokens = min(float64(burst), l.tokens+float64(d)/float64(every))
	}
	l.at = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// flooding учитывает отброшенный Status; true — за floodWindow их набралось limit (устойчивое превышение).
func (l *statusLimit) flooding(now time.Time, limit int) bool {
	if l.dropAt.IsZero() || now.Sub(l.dropAt) >= floodWindow {
		l.dropAt, l.dropped = now, 0
	}
	l.dropped++
	return l.dropped >= limit
}
