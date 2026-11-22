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
//
// Тот же потолок — у AgentEvent и CommandResult (ревью 22.11.2026): ПК с действующим сертификатом потоком событий
// со случайным event_id заставлял сервер ходить в Valkey и обработчик событий на каждое сообщение, а потоком
// CommandResult со случайным command_id — открывать транзакцию Postgres. У каждого вида сообщений своё ведро:
//
//   - AgentEvent: всплеск EventBurst, пополнение одним за EventEvery. Честный агент после переподключения досылает
//     весь журнал неподтверждённых событий подряд (EventJournalStore.Limit = 1000), дальше события редки (вход и
//     выход гостя, конец сессии, ошибки). Отброшенное событие не подтверждается (EventAck не уходит) и не теряется:
//     агент повторит его после переподключения.
//   - CommandResult неизвестной этому потоку команды (её нет среди отправленных и неподтверждённых): всплеск
//     ResultBurst, пополнение одним за ResultEvery. Честный агент после переподключения досылает не больше 32
//     недоставленных итогов (CommandJournalStore.Undelivered). Итоги команд, отправленных в этом потоке, и ответы на
//     Ping ведро не расходуют: их число ограничено числом команд сервера.
const (
	// DefaultStatusBurst — всплеск Status на поток.
	DefaultStatusBurst = 10
	// DefaultStatusFloodDrops — отброшенных сообщений одного вида за floodWindow, после которых поток закрывается.
	DefaultStatusFloodDrops = 300
	// DefaultEventBurst — всплеск AgentEvent на поток: журнал агента (1000 событий) с запасом.
	DefaultEventBurst = 1200
	// DefaultEventEvery — пополнение ведра AgentEvent.
	DefaultEventEvery = time.Second
	// DefaultResultBurst — всплеск CommandResult неизвестных потоку команд: 32 недоставленных итога агента с запасом.
	DefaultResultBurst = 64
	// DefaultResultEvery — пополнение ведра CommandResult.
	DefaultResultEvery = 2 * time.Second
	floodWindow        = time.Minute
)

// msgLimit — состояние потолка одного вида сообщений одного потока; читает и пишет только горутина чтения стрима.
type msgLimit struct {
	tokens  float64
	at      time.Time // когда ведро пересчитано; ноль — ведро ещё полное
	dropAt  time.Time // начало окна подсчёта отброшенных
	dropped int
}

// allow списывает одно сообщение; false — потолок исчерпан.
func (l *msgLimit) allow(now time.Time, burst int, every time.Duration) bool {
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

// flooding учитывает отброшенное сообщение; true — за floodWindow их набралось limit (устойчивое превышение).
func (l *msgLimit) flooding(now time.Time, limit int) bool {
	if l.dropAt.IsZero() || now.Sub(l.dropAt) >= floodWindow {
		l.dropAt, l.dropped = now, 0
	}
	l.dropped++
	return l.dropped >= limit
}
