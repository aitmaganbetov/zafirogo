// Контракт агента ПК Zafiro ↔ сервер zafirogo. Пакет zafiro.agent.v1.
//
// =====================================================================================
// ОБЩИЕ ПРАВИЛА (действуют для всех файлов пакета: common, enrollment, agent, guest,
// shop, social). Подробности и схемы — docs/agent-protocol.md.
// =====================================================================================
//
// 1. Транспорт и аутентификация
//    * gRPC поверх HTTP/2 + TLS 1.3. Сервер проверяется по встроенному в агент корню CA
//      Zafiro (pinning), а не по системному хранилищу Windows.
//    * EnrollmentService.Enroll — без клиентского сертификата (одноразовый код).
//    * Все остальные сервисы — mTLS: клиентский сертификат, выпущенный Enroll/RenewCertificate.
//      ПК (computer_id, club_id) сервер берёт ТОЛЬКО из сертификата; в сообщениях computer_id
//      «своего» ПК не передаётся (в старом API агент слал computer_id и X-Agent-Token, а
//      контроллеры сверяли их — GuestShopController, AgentTariffController::purchase и т.п.).
//    * Отозванный сертификат (панель клуба: «Отключить ПК», удаление карточки, повторный Enroll)
//      отклоняется при рукопожатии, открытый стрим сервер закрывает со статусом
//      UNAUTHENTICATED + ERROR_CODE_CERTIFICATE_REVOKED.
//    * Действия гостя (GuestService, ShopService, SocialService, GuestAuthService.Logout) —
//      дополнительно токен гостя в метаданных: `authorization: Bearer <access_token>`.
//      Токен выдаётся GuestAuthService.Login/Register/WatchQrLogin, привязан к ПК (к серийному
//      номеру сертификата) и на другом ПК недействителен. Срок — до 12 часов, продление не нужно:
//      токен живёт, пока гость сидит за ПК (см. open-questions/api.md про срок).
//
// 2. Метаданные запроса
//    * `zafiro-protocol-version: 1` — в каждом запросе (в стриме — также поле Hello.protocol_version).
//    * `zafiro-agent-version: 2.0.0` — версия агента.
//    * `zafiro-locale: ru|kk|en` — язык текстов ошибок (google.rpc.LocalizedMessage).
//      Старый аналог: LocaleResolver::forAgentRequest.
//
// 3. Версионирование
//    * Пакет zafiro.agent.v1 меняется только обратно совместимо: новые поля, новые значения
//      enum в конец, новые RPC и команды. Номера полей не переиспользуются (reserved).
//    * protocol_version (uint32) — минорная версия протокола внутри v1. Сервер в Welcome
//      сообщает min/max поддерживаемые; агент ниже min получает
//      ERROR_CODE_PROTOCOL_VERSION_UNSUPPORTED и ждёт обновления.
//    * Несовместимые изменения — новый пакет zafiro.agent.v2; сервер держит v1 и v2
//      параллельно, пока все агенты не обновятся.
//
// 4. Ошибки
//    * unary: gRPC-статус + google.rpc.ErrorInfo{reason = имя ErrorCode, domain =
//      "agent.zafiro.kz"} (+ BadRequest / RetryInfo / LocalizedMessage). См. enum ErrorCode
//      в common.proto — там же соответствие gRPC-кодам.
//    * стрим: ошибки выполнения команд — CommandResult.error_code; фатальные ошибки стрима —
//      закрытие стрима gRPC-статусом с ErrorInfo.
//
// 5. Идемпотентность
//    * Команды сервера: у каждой Command есть command_id (UUID v7, уникален глобально).
//      Агент хранит на диске (ProgramData, служба) id последних 256 выполненных команд и
//      их результаты: повтор команды с тем же command_id НЕ выполняется второй раз — агент
//      повторно отправляет сохранённый CommandResult.
//    * Unary-RPC, двигающие деньги или состояние ПК (покупка тарифа/минут/пакета,
//      завершение сессии, пересадка, заказ в магазине, перевод другу, сообщение в чат),
//      ОБЯЗАТЕЛЬНО несут idempotency_key (UUID v4/v7, генерирует агент на одно действие
//      пользователя, при повторе по сети — тот же ключ). Сервер хранит
//      (computer_id, idempotency_key) → хеш запроса + ответ 24 часа (как /api/v1:
//      app/Http/Middleware/Api/ApiIdempotency.php, config integrations.idempotency_retention_hours):
//        - тот же ключ и тот же запрос — возвращается сохранённый ответ, действие не повторяется;
//        - тот же ключ, другой запрос — ERROR_CODE_IDEMPOTENCY_KEY_REUSED;
//        - запрос ещё выполняется — ERROR_CODE_IDEMPOTENCY_IN_PROGRESS (ABORTED);
//        - ответы INTERNAL/UNAVAILABLE не сохраняются (можно повторить).
//      Ключ идемпотентности становится ключом записи в журнале денег (ledger).
//    * События агента (AgentEvent) несут event_id (UUID); сервер дедуплицирует их
//      по (computer_id, event_id) и подтверждает EventAck.
//
// 6. Доставка команд и reconnect (seq)
//    * У каждой «долговременной» команды есть seq — монотонно растущий номер в рамках ПК
//      (uint64, пропуски допустимы). Сервер хранит невыполненные команды до expires_at.
//    * Агент после выполнения команды сохраняет last_command_seq на диск (атомарно, до
//      отправки CommandResult) и шлёт его в Hello при каждом подключении.
//    * После Hello сервер отвечает Welcome (полный снимок: конфигурация клуба, активная
//      сессия, балансы, версия переводов), затем досылает все неистёкшие команды с
//      seq > last_command_seq по возрастанию seq. Команда с seq ≤ last_command_seq —
//      дубль: агент не выполняет, но отвечает сохранённым результатом (или DUPLICATE).
//    * «Мгновенные» команды (Ping, BalanceChanged, GuestCountersChanged, ChatMessageReceived,
//      TranslationsChanged, ConfigChanged) имеют seq = 0, не досылаются после reconnect —
//      их состояние приходит снимком в Welcome.
//    * Агент выполняет долговременные команды строго по возрастанию seq.
//
// 7. Heartbeat / Status
//    * Агент шлёт Status сразу после Welcome, при любом изменении состояния (вход/выход гостя,
//      начало/конец сессии, блокировка) и не реже Welcome.status_interval (по умолчанию 60 с;
//      в Laravel heartbeat шёл каждые 30 с — config club.computer_heartbeat_interval_seconds).
//    * Не чаще 1 Status в 2 секунды (сервер отбрасывает лишние, стрим не закрывает).
//    * ПК считается в сети, пока открыт стрим (а не по last_seen_at; в Laravel —
//      ClubComputer::HEARTBEAT_TIMEOUT_SECONDS = 90). Сервер держит статус в памяти/Valkey,
//      в Postgres пишет только изменения состояния (правило из CLAUDE.md).
//    * Живость соединения — HTTP/2 keepalive: клиент PING каждые 30 с, таймаут 10 с;
//      сервер разрешает PING не чаще раза в 20 с (EnforcementPolicy.MinTime).
//
// 8. Reconnect и backoff
//    * При разрыве агент переподключается с экспоненциальной задержкой и full jitter:
//      delay = random(0, min(60 с, 1 с * 2^attempt)); attempt сбрасывается после стрима,
//      прожившего ≥ 60 с. Если сервер вернул google.rpc.RetryInfo — не раньше retry_delay.
//    * UNAVAILABLE / RESOURCE_EXHAUSTED / ABORTED / DEADLINE_EXCEEDED — повторяемые.
//      UNAUTHENTICATED с CERTIFICATE_REVOKED / CERTIFICATE_EXPIRED — без повторов (нужна
//      новая регистрация). PROTOCOL_VERSION_UNSUPPORTED — повтор не чаще раза в 10 минут.
//    * При плановом перезапуске сервер шлёт Command.drain (GOAWAY на уровне HTTP/2):
//      агент переподключается со случайной задержкой 0–30 с (растягивает нагрузку 50 000 ПК).
//
// 9. Лимиты
//    * Максимальный размер сообщения — 1 МиБ в обе стороны (переводы агента — до 1 МиБ, как
//      проверка Content-Length в ZafiroApiClient.GetAgentTranslationsAsync).
//    * Одновременно один стрим Connect на сертификат: новый стрим закрывает прежний
//      (ABORTED, reason "replaced").
//    * Deadline unary-вызовов по умолчанию 15 с (вход/покупки), 5 с (чтение).
//    * Лимиты частоты unary-RPC повторяют throttle:* старого API — указаны у каждого RPC.
//
// 10. Деньги и время: суммы int64 в тиынах (`*_tiyn`), время — google.protobuf.Timestamp UTC.
//     Агент считает конец сессии по часам сервера (Welcome.server_time, Ping), как сейчас
//     server_time в ответе heartbeat.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.12
// 	protoc        (unknown)
// source: agent/v1/agent.proto

package agentv1

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// Состояние диска по SMART (MSFT_PhysicalDisk.HealthStatus / предсказание отказа).
type DiskSmart int32

const (
	// Не измерено.
	DiskSmart_DISK_SMART_UNSPECIFIED DiskSmart = 0
	// Исправен.
	DiskSmart_DISK_SMART_OK DiskSmart = 1
	// Предупреждение (деградация, рост переназначенных секторов).
	DiskSmart_DISK_SMART_WARNING DiskSmart = 2
	// Предсказан отказ.
	DiskSmart_DISK_SMART_FAILING DiskSmart = 3
)

// Enum value maps for DiskSmart.
var (
	DiskSmart_name = map[int32]string{
		0: "DISK_SMART_UNSPECIFIED",
		1: "DISK_SMART_OK",
		2: "DISK_SMART_WARNING",
		3: "DISK_SMART_FAILING",
	}
	DiskSmart_value = map[string]int32{
		"DISK_SMART_UNSPECIFIED": 0,
		"DISK_SMART_OK":          1,
		"DISK_SMART_WARNING":     2,
		"DISK_SMART_FAILING":     3,
	}
)

func (x DiskSmart) Enum() *DiskSmart {
	p := new(DiskSmart)
	*p = x
	return p
}

func (x DiskSmart) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (DiskSmart) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[0].Descriptor()
}

func (DiskSmart) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[0]
}

func (x DiskSmart) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use DiskSmart.Descriptor instead.
func (DiskSmart) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{0}
}

// Вид периферии.
type PeripheralKind int32

const (
	// Не задано.
	PeripheralKind_PERIPHERAL_KIND_UNSPECIFIED PeripheralKind = 0
	// Мышь.
	PeripheralKind_PERIPHERAL_KIND_MOUSE PeripheralKind = 1
	// Клавиатура.
	PeripheralKind_PERIPHERAL_KIND_KEYBOARD PeripheralKind = 2
	// Гарнитура / наушники (аудиоустройство вывода, кроме встроенного).
	PeripheralKind_PERIPHERAL_KIND_HEADSET PeripheralKind = 3
	// Монитор.
	PeripheralKind_PERIPHERAL_KIND_MONITOR PeripheralKind = 4
)

// Enum value maps for PeripheralKind.
var (
	PeripheralKind_name = map[int32]string{
		0: "PERIPHERAL_KIND_UNSPECIFIED",
		1: "PERIPHERAL_KIND_MOUSE",
		2: "PERIPHERAL_KIND_KEYBOARD",
		3: "PERIPHERAL_KIND_HEADSET",
		4: "PERIPHERAL_KIND_MONITOR",
	}
	PeripheralKind_value = map[string]int32{
		"PERIPHERAL_KIND_UNSPECIFIED": 0,
		"PERIPHERAL_KIND_MOUSE":       1,
		"PERIPHERAL_KIND_KEYBOARD":    2,
		"PERIPHERAL_KIND_HEADSET":     3,
		"PERIPHERAL_KIND_MONITOR":     4,
	}
)

func (x PeripheralKind) Enum() *PeripheralKind {
	p := new(PeripheralKind)
	*p = x
	return p
}

func (x PeripheralKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (PeripheralKind) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[1].Descriptor()
}

func (PeripheralKind) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[1]
}

func (x PeripheralKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use PeripheralKind.Descriptor instead.
func (PeripheralKind) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{1}
}

// Причина окончания сессии.
type SessionEndReason int32

const (
	// Не задано.
	SessionEndReason_SESSION_END_REASON_UNSPECIFIED SessionEndReason = 0
	// Оплаченное время вышло.
	SessionEndReason_SESSION_END_REASON_EXPIRED SessionEndReason = 1
	// Гость завершил досрочно (GuestService.EndSession).
	SessionEndReason_SESSION_END_REASON_GUEST SessionEndReason = 2
	// Сотрудник завершил (панель, приложение сотрудника).
	SessionEndReason_SESSION_END_REASON_STAFF SessionEndReason = 3
	// Гость пересел на другой ПК (GuestService.TransferSeat).
	SessionEndReason_SESSION_END_REASON_SEAT_TRANSFER SessionEndReason = 4
	// ПК выключили или перезагрузили.
	SessionEndReason_SESSION_END_REASON_POWER SessionEndReason = 5
)

// Enum value maps for SessionEndReason.
var (
	SessionEndReason_name = map[int32]string{
		0: "SESSION_END_REASON_UNSPECIFIED",
		1: "SESSION_END_REASON_EXPIRED",
		2: "SESSION_END_REASON_GUEST",
		3: "SESSION_END_REASON_STAFF",
		4: "SESSION_END_REASON_SEAT_TRANSFER",
		5: "SESSION_END_REASON_POWER",
	}
	SessionEndReason_value = map[string]int32{
		"SESSION_END_REASON_UNSPECIFIED":   0,
		"SESSION_END_REASON_EXPIRED":       1,
		"SESSION_END_REASON_GUEST":         2,
		"SESSION_END_REASON_STAFF":         3,
		"SESSION_END_REASON_SEAT_TRANSFER": 4,
		"SESSION_END_REASON_POWER":         5,
	}
)

func (x SessionEndReason) Enum() *SessionEndReason {
	p := new(SessionEndReason)
	*p = x
	return p
}

func (x SessionEndReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (SessionEndReason) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[2].Descriptor()
}

func (SessionEndReason) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[2]
}

func (x SessionEndReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use SessionEndReason.Descriptor instead.
func (SessionEndReason) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{2}
}

// Способ входа гостя.
type SignInMethod int32

const (
	// Не задано.
	SignInMethod_SIGN_IN_METHOD_UNSPECIFIED SignInMethod = 0
	// Телефон и пароль.
	SignInMethod_SIGN_IN_METHOD_PASSWORD SignInMethod = 1
	// QR-код с телефона.
	SignInMethod_SIGN_IN_METHOD_QR SignInMethod = 2
	// Регистрация на ПК.
	SignInMethod_SIGN_IN_METHOD_REGISTRATION SignInMethod = 3
	// Гостевая сессия с кассы без аккаунта.
	SignInMethod_SIGN_IN_METHOD_CASH_DESK SignInMethod = 4
)

// Enum value maps for SignInMethod.
var (
	SignInMethod_name = map[int32]string{
		0: "SIGN_IN_METHOD_UNSPECIFIED",
		1: "SIGN_IN_METHOD_PASSWORD",
		2: "SIGN_IN_METHOD_QR",
		3: "SIGN_IN_METHOD_REGISTRATION",
		4: "SIGN_IN_METHOD_CASH_DESK",
	}
	SignInMethod_value = map[string]int32{
		"SIGN_IN_METHOD_UNSPECIFIED":  0,
		"SIGN_IN_METHOD_PASSWORD":     1,
		"SIGN_IN_METHOD_QR":           2,
		"SIGN_IN_METHOD_REGISTRATION": 3,
		"SIGN_IN_METHOD_CASH_DESK":    4,
	}
)

func (x SignInMethod) Enum() *SignInMethod {
	p := new(SignInMethod)
	*p = x
	return p
}

func (x SignInMethod) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (SignInMethod) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[3].Descriptor()
}

func (SignInMethod) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[3]
}

func (x SignInMethod) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use SignInMethod.Descriptor instead.
func (SignInMethod) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{3}
}

// Причина выхода гостя.
type SignOutReason int32

const (
	// Не задано.
	SignOutReason_SIGN_OUT_REASON_UNSPECIFIED SignOutReason = 0
	// Гость нажал «Выйти».
	SignOutReason_SIGN_OUT_REASON_USER SignOutReason = 1
	// Бездействие без оплаченной сессии (club idle_logout_minutes, GuestFeatures::idleLogoutMinutes).
	SignOutReason_SIGN_OUT_REASON_IDLE SignOutReason = 2
	// По команде сервера (SignOutGuest).
	SignOutReason_SIGN_OUT_REASON_SERVER SignOutReason = 3
	// Токен гостя отклонён сервером.
	SignOutReason_SIGN_OUT_REASON_TOKEN_INVALID SignOutReason = 4
)

// Enum value maps for SignOutReason.
var (
	SignOutReason_name = map[int32]string{
		0: "SIGN_OUT_REASON_UNSPECIFIED",
		1: "SIGN_OUT_REASON_USER",
		2: "SIGN_OUT_REASON_IDLE",
		3: "SIGN_OUT_REASON_SERVER",
		4: "SIGN_OUT_REASON_TOKEN_INVALID",
	}
	SignOutReason_value = map[string]int32{
		"SIGN_OUT_REASON_UNSPECIFIED":   0,
		"SIGN_OUT_REASON_USER":          1,
		"SIGN_OUT_REASON_IDLE":          2,
		"SIGN_OUT_REASON_SERVER":        3,
		"SIGN_OUT_REASON_TOKEN_INVALID": 4,
	}
)

func (x SignOutReason) Enum() *SignOutReason {
	p := new(SignOutReason)
	*p = x
	return p
}

func (x SignOutReason) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (SignOutReason) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[4].Descriptor()
}

func (SignOutReason) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[4]
}

func (x SignOutReason) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use SignOutReason.Descriptor instead.
func (SignOutReason) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{4}
}

// Итог выполнения команды.
type CommandStatus int32

const (
	// Не задано.
	CommandStatus_COMMAND_STATUS_UNSPECIFIED CommandStatus = 0
	// Команда принята, выполнение займёт время (например, загрузка обновления). Итог придёт
	// вторым CommandResult с тем же command_id.
	CommandStatus_COMMAND_STATUS_ACCEPTED CommandStatus = 1
	// Выполнена.
	CommandStatus_COMMAND_STATUS_SUCCEEDED CommandStatus = 2
	// Не выполнена (см. error_code).
	CommandStatus_COMMAND_STATUS_FAILED CommandStatus = 3
	// Повтор уже выполненной команды: не выполнялась второй раз.
	CommandStatus_COMMAND_STATUS_DUPLICATE CommandStatus = 4
)

// Enum value maps for CommandStatus.
var (
	CommandStatus_name = map[int32]string{
		0: "COMMAND_STATUS_UNSPECIFIED",
		1: "COMMAND_STATUS_ACCEPTED",
		2: "COMMAND_STATUS_SUCCEEDED",
		3: "COMMAND_STATUS_FAILED",
		4: "COMMAND_STATUS_DUPLICATE",
	}
	CommandStatus_value = map[string]int32{
		"COMMAND_STATUS_UNSPECIFIED": 0,
		"COMMAND_STATUS_ACCEPTED":    1,
		"COMMAND_STATUS_SUCCEEDED":   2,
		"COMMAND_STATUS_FAILED":      3,
		"COMMAND_STATUS_DUPLICATE":   4,
	}
)

func (x CommandStatus) Enum() *CommandStatus {
	p := new(CommandStatus)
	*p = x
	return p
}

func (x CommandStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (CommandStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[5].Descriptor()
}

func (CommandStatus) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[5]
}

func (x CommandStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use CommandStatus.Descriptor instead.
func (CommandStatus) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{5}
}

// Действие после окончания сессии (настройка клуба). Старый аналог:
// ClubComputerController::afterSessionAction() → {action: none|restart|shutdown, delay_seconds}.
type AfterSessionAction int32

const (
	// Не задано (= ничего не делать).
	AfterSessionAction_AFTER_SESSION_ACTION_UNSPECIFIED AfterSessionAction = 0
	// Ничего.
	AfterSessionAction_AFTER_SESSION_ACTION_NONE AfterSessionAction = 1
	// Перезагрузить ПК.
	AfterSessionAction_AFTER_SESSION_ACTION_RESTART AfterSessionAction = 2
	// Выключить ПК.
	AfterSessionAction_AFTER_SESSION_ACTION_SHUTDOWN AfterSessionAction = 3
)

// Enum value maps for AfterSessionAction.
var (
	AfterSessionAction_name = map[int32]string{
		0: "AFTER_SESSION_ACTION_UNSPECIFIED",
		1: "AFTER_SESSION_ACTION_NONE",
		2: "AFTER_SESSION_ACTION_RESTART",
		3: "AFTER_SESSION_ACTION_SHUTDOWN",
	}
	AfterSessionAction_value = map[string]int32{
		"AFTER_SESSION_ACTION_UNSPECIFIED": 0,
		"AFTER_SESSION_ACTION_NONE":        1,
		"AFTER_SESSION_ACTION_RESTART":     2,
		"AFTER_SESSION_ACTION_SHUTDOWN":    3,
	}
)

func (x AfterSessionAction) Enum() *AfterSessionAction {
	p := new(AfterSessionAction)
	*p = x
	return p
}

func (x AfterSessionAction) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (AfterSessionAction) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[6].Descriptor()
}

func (AfterSessionAction) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[6]
}

func (x AfterSessionAction) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use AfterSessionAction.Descriptor instead.
func (AfterSessionAction) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{6}
}

// Вид картинки клуба.
type ImageKind int32

const (
	// Не задано (ошибка VALIDATION_FAILED).
	ImageKind_IMAGE_KIND_UNSPECIFIED ImageKind = 0
	// Логотип клуба ПК (ClubConfig.logo_url; id не нужен).
	ImageKind_IMAGE_KIND_CLUB_LOGO ImageKind = 1
	// Картинка экрана входа клуба ПК (ClubConfig.signin_image_url; id не нужен).
	ImageKind_IMAGE_KIND_SIGNIN ImageKind = 2
	// Картинка новости (NewsItem.news_id): клуба ПК или платформы.
	ImageKind_IMAGE_KIND_NEWS ImageKind = 3
)

// Enum value maps for ImageKind.
var (
	ImageKind_name = map[int32]string{
		0: "IMAGE_KIND_UNSPECIFIED",
		1: "IMAGE_KIND_CLUB_LOGO",
		2: "IMAGE_KIND_SIGNIN",
		3: "IMAGE_KIND_NEWS",
	}
	ImageKind_value = map[string]int32{
		"IMAGE_KIND_UNSPECIFIED": 0,
		"IMAGE_KIND_CLUB_LOGO":   1,
		"IMAGE_KIND_SIGNIN":      2,
		"IMAGE_KIND_NEWS":        3,
	}
)

func (x ImageKind) Enum() *ImageKind {
	p := new(ImageKind)
	*p = x
	return p
}

func (x ImageKind) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ImageKind) Descriptor() protoreflect.EnumDescriptor {
	return file_agent_v1_agent_proto_enumTypes[7].Descriptor()
}

func (ImageKind) Type() protoreflect.EnumType {
	return &file_agent_v1_agent_proto_enumTypes[7]
}

func (x ImageKind) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ImageKind.Descriptor instead.
func (ImageKind) EnumDescriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{7}
}

// Сообщение агента в стриме Connect.
type AgentMessage struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Содержимое (ровно одно).
	//
	// Types that are valid to be assigned to Payload:
	//
	//	*AgentMessage_Hello
	//	*AgentMessage_Status
	//	*AgentMessage_Event
	//	*AgentMessage_CommandResult
	Payload       isAgentMessage_Payload `protobuf_oneof:"payload"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AgentMessage) Reset() {
	*x = AgentMessage{}
	mi := &file_agent_v1_agent_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AgentMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AgentMessage) ProtoMessage() {}

func (x *AgentMessage) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AgentMessage.ProtoReflect.Descriptor instead.
func (*AgentMessage) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{0}
}

func (x *AgentMessage) GetPayload() isAgentMessage_Payload {
	if x != nil {
		return x.Payload
	}
	return nil
}

func (x *AgentMessage) GetHello() *Hello {
	if x != nil {
		if x, ok := x.Payload.(*AgentMessage_Hello); ok {
			return x.Hello
		}
	}
	return nil
}

func (x *AgentMessage) GetStatus() *Status {
	if x != nil {
		if x, ok := x.Payload.(*AgentMessage_Status); ok {
			return x.Status
		}
	}
	return nil
}

func (x *AgentMessage) GetEvent() *AgentEvent {
	if x != nil {
		if x, ok := x.Payload.(*AgentMessage_Event); ok {
			return x.Event
		}
	}
	return nil
}

func (x *AgentMessage) GetCommandResult() *CommandResult {
	if x != nil {
		if x, ok := x.Payload.(*AgentMessage_CommandResult); ok {
			return x.CommandResult
		}
	}
	return nil
}

type isAgentMessage_Payload interface {
	isAgentMessage_Payload()
}

type AgentMessage_Hello struct {
	// Первое сообщение стрима (обязательно, ровно один раз).
	Hello *Hello `protobuf:"bytes,1,opt,name=hello,proto3,oneof"`
}

type AgentMessage_Status struct {
	// Статус ПК (heartbeat).
	Status *Status `protobuf:"bytes,2,opt,name=status,proto3,oneof"`
}

type AgentMessage_Event struct {
	// Событие на ПК.
	Event *AgentEvent `protobuf:"bytes,3,opt,name=event,proto3,oneof"`
}

type AgentMessage_CommandResult struct {
	// Результат (ack) команды сервера.
	CommandResult *CommandResult `protobuf:"bytes,4,opt,name=command_result,json=commandResult,proto3,oneof"`
}

func (*AgentMessage_Hello) isAgentMessage_Payload() {}

func (*AgentMessage_Status) isAgentMessage_Payload() {}

func (*AgentMessage_Event) isAgentMessage_Payload() {}

func (*AgentMessage_CommandResult) isAgentMessage_Payload() {}

// Приветствие при открытии стрима.
type Hello struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Версия протокола внутри v1. Текущая — 1.
	ProtocolVersion uint32 `protobuf:"varint,1,opt,name=protocol_version,json=protocolVersion,proto3" json:"protocol_version,omitempty"`
	// Версия агента (SemVer).
	AgentVersion string `protobuf:"bytes,2,opt,name=agent_version,json=agentVersion,proto3" json:"agent_version,omitempty"`
	// seq последней ВЫПОЛНЕННОЙ долговременной команды (0 — ни одной). По нему сервер
	// досылает пропущенные команды.
	LastCommandSeq uint64 `protobuf:"varint,3,opt,name=last_command_seq,json=lastCommandSeq,proto3" json:"last_command_seq,omitempty"`
	// UUID текущей загрузки Windows (новый после каждой перезагрузки). Новый boot_id сервер
	// трактует как «свежую загрузку»: сбрасывает гостевую сессию с кассы, если она была
	// (старый аналог — fresh_boot в heartbeat, ClubComputerSessions::resetGuestSession).
	BootId string `protobuf:"bytes,4,opt,name=boot_id,json=bootId,proto3" json:"boot_id,omitempty"`
	// Сведения о железе: обязательно в первом Hello после загрузки, дальше — если изменились.
	Device *DeviceInfo `protobuf:"bytes,5,opt,name=device,proto3" json:"device,omitempty"`
	// Что умеет этот агент.
	Capabilities *AgentCapabilities `protobuf:"bytes,6,opt,name=capabilities,proto3" json:"capabilities,omitempty"`
	// Текущее состояние (то же, что первый Status).
	Status *Status `protobuf:"bytes,7,opt,name=status,proto3" json:"status,omitempty"`
	// Версия переводов, которая уже есть у агента (0 — только встроенные).
	TranslationsVersion int64 `protobuf:"varint,8,opt,name=translations_version,json=translationsVersion,proto3" json:"translations_version,omitempty"`
	// Язык интерфейса, выбранный на ПК (ru | kk | en ...).
	Locale        string `protobuf:"bytes,9,opt,name=locale,proto3" json:"locale,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Hello) Reset() {
	*x = Hello{}
	mi := &file_agent_v1_agent_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Hello) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Hello) ProtoMessage() {}

func (x *Hello) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Hello.ProtoReflect.Descriptor instead.
func (*Hello) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{1}
}

func (x *Hello) GetProtocolVersion() uint32 {
	if x != nil {
		return x.ProtocolVersion
	}
	return 0
}

func (x *Hello) GetAgentVersion() string {
	if x != nil {
		return x.AgentVersion
	}
	return ""
}

func (x *Hello) GetLastCommandSeq() uint64 {
	if x != nil {
		return x.LastCommandSeq
	}
	return 0
}

func (x *Hello) GetBootId() string {
	if x != nil {
		return x.BootId
	}
	return ""
}

func (x *Hello) GetDevice() *DeviceInfo {
	if x != nil {
		return x.Device
	}
	return nil
}

func (x *Hello) GetCapabilities() *AgentCapabilities {
	if x != nil {
		return x.Capabilities
	}
	return nil
}

func (x *Hello) GetStatus() *Status {
	if x != nil {
		return x.Status
	}
	return nil
}

func (x *Hello) GetTranslationsVersion() int64 {
	if x != nil {
		return x.TranslationsVersion
	}
	return 0
}

func (x *Hello) GetLocale() string {
	if x != nil {
		return x.Locale
	}
	return ""
}

// Возможности агента: сервер не шлёт команды, которых агент не умеет
// (в Laravel то же решалось версией агента: ComputerPower::AGENT_VERSION / MESSAGE_AGENT_VERSION
// и флагами commands / can_update в heartbeat).
type AgentCapabilities struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Служба может сама установить обновление (UpdateAvailable).
	SelfUpdate bool `protobuf:"varint,1,opt,name=self_update,json=selfUpdate,proto3" json:"self_update,omitempty"`
	// Агент может разбудить другие ПК (Wake-on-LAN).
	WakeOnLan bool `protobuf:"varint,2,opt,name=wake_on_lan,json=wakeOnLan,proto3" json:"wake_on_lan,omitempty"`
	// Агент выполняет Reboot/Shutdown.
	PowerControl bool `protobuf:"varint,3,opt,name=power_control,json=powerControl,proto3" json:"power_control,omitempty"`
	// Агент показывает ShowMessage.
	ShowMessage bool `protobuf:"varint,4,opt,name=show_message,json=showMessage,proto3" json:"show_message,omitempty"`
	// Агент поддерживает техрежим (EnterServiceMode).
	ServiceMode   bool `protobuf:"varint,5,opt,name=service_mode,json=serviceMode,proto3" json:"service_mode,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AgentCapabilities) Reset() {
	*x = AgentCapabilities{}
	mi := &file_agent_v1_agent_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AgentCapabilities) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AgentCapabilities) ProtoMessage() {}

func (x *AgentCapabilities) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AgentCapabilities.ProtoReflect.Descriptor instead.
func (*AgentCapabilities) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{2}
}

func (x *AgentCapabilities) GetSelfUpdate() bool {
	if x != nil {
		return x.SelfUpdate
	}
	return false
}

func (x *AgentCapabilities) GetWakeOnLan() bool {
	if x != nil {
		return x.WakeOnLan
	}
	return false
}

func (x *AgentCapabilities) GetPowerControl() bool {
	if x != nil {
		return x.PowerControl
	}
	return false
}

func (x *AgentCapabilities) GetShowMessage() bool {
	if x != nil {
		return x.ShowMessage
	}
	return false
}

func (x *AgentCapabilities) GetServiceMode() bool {
	if x != nil {
		return x.ServiceMode
	}
	return false
}

// Статус ПК (сжатый heartbeat).
type Status struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Состояние агента.
	State ComputerState `protobuf:"varint,1,opt,name=state,proto3,enum=zafiro.agent.v1.ComputerState" json:"state,omitempty"`
	// ID сессии, которую агент сейчас показывает (только при IN_SESSION).
	SessionId string `protobuf:"bytes,2,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	// Кто вошёл на ПК (0 — никто).
	SignedInUserId int64 `protobuf:"varint,3,opt,name=signed_in_user_id,json=signedInUserId,proto3" json:"signed_in_user_id,omitempty"`
	// Загрузка ПК (для карты клуба и диагностики).
	Load *ComputerLoad `protobuf:"bytes,4,opt,name=load,proto3" json:"load,omitempty"`
	// Время на ПК в момент отправки (для оценки рассинхрона часов).
	ObservedAt *timestamppb.Timestamp `protobuf:"bytes,5,opt,name=observed_at,json=observedAt,proto3" json:"observed_at,omitempty"`
	// Здоровье ПК (Этап 7): температуры, диски, периферия, сеть, сбои. Агент кладёт его в Status раз в
	// status_interval и сразу — при подключении/отключении периферии или смене SMART диска; между ними
	// Status без health не означает «здоровье неизвестно» — сервер держит последнее присланное.
	Health        *ComputerHealth `protobuf:"bytes,6,opt,name=health,proto3" json:"health,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Status) Reset() {
	*x = Status{}
	mi := &file_agent_v1_agent_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Status) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Status) ProtoMessage() {}

func (x *Status) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Status.ProtoReflect.Descriptor instead.
func (*Status) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{3}
}

func (x *Status) GetState() ComputerState {
	if x != nil {
		return x.State
	}
	return ComputerState_COMPUTER_STATE_UNSPECIFIED
}

func (x *Status) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *Status) GetSignedInUserId() int64 {
	if x != nil {
		return x.SignedInUserId
	}
	return 0
}

func (x *Status) GetLoad() *ComputerLoad {
	if x != nil {
		return x.Load
	}
	return nil
}

func (x *Status) GetObservedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ObservedAt
	}
	return nil
}

func (x *Status) GetHealth() *ComputerHealth {
	if x != nil {
		return x.Health
	}
	return nil
}

// Здоровье ПК. Ни одно поле не обязательно: агент заполняет то, что смог измерить на этом ПК
// (без драйверов ядра — только штатные источники Windows и драйвера видеокарты).
type ComputerHealth struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Температура CPU, десятые доли °C; 0 — не измерено (на многих платах Windows её не отдаёт).
	CpuTempDecicelsius int32 `protobuf:"varint,1,opt,name=cpu_temp_decicelsius,json=cpuTempDecicelsius,proto3" json:"cpu_temp_decicelsius,omitempty"`
	// Температура GPU, десятые доли °C; 0 — не измерено.
	GpuTempDecicelsius int32 `protobuf:"varint,2,opt,name=gpu_temp_decicelsius,json=gpuTempDecicelsius,proto3" json:"gpu_temp_decicelsius,omitempty"`
	// Загрузка GPU, сотые доли процента (0..10000); есть, только если gpu_measured.
	GpuLoadBasisPoints uint32 `protobuf:"varint,3,opt,name=gpu_load_basis_points,json=gpuLoadBasisPoints,proto3" json:"gpu_load_basis_points,omitempty"`
	// Занятая память GPU, сотые доли процента (0..10000); есть, только если gpu_measured.
	GpuMemoryBasisPoints uint32 `protobuf:"varint,4,opt,name=gpu_memory_basis_points,json=gpuMemoryBasisPoints,proto3" json:"gpu_memory_basis_points,omitempty"`
	// Загрузка и память GPU измерены (иначе поля 3–4 не заполнены).
	GpuMeasured bool `protobuf:"varint,5,opt,name=gpu_measured,json=gpuMeasured,proto3" json:"gpu_measured,omitempty"`
	// Локальные диски (не сетевые, не съёмные).
	Disks []*DiskHealth `protobuf:"bytes,6,rep,name=disks,proto3" json:"disks,omitempty"`
	// Периферия, которую агент проверяет (мышь, клавиатура, гарнитура, монитор).
	Peripherals []*Peripheral `protobuf:"bytes,7,rep,name=peripherals,proto3" json:"peripherals,omitempty"`
	// Скорость основного сетевого подключения, Мбит/с; 0 — не измерено.
	LinkSpeedMbps uint32 `protobuf:"varint,8,opt,name=link_speed_mbps,json=linkSpeedMbps,proto3" json:"link_speed_mbps,omitempty"`
	// Сколько секунд Windows работает с последней загрузки.
	UptimeSeconds uint64 `protobuf:"varint,9,opt,name=uptime_seconds,json=uptimeSeconds,proto3" json:"uptime_seconds,omitempty"`
	// Внезапных выключений за 7 суток (журнал System: Kernel-Power 41, EventLog 6008). По ним — проблема unstable; в
	// почасовую историю не идут (там — счётчики перед загрузкой).
	UnexpectedShutdowns_7D uint32 `protobuf:"varint,10,opt,name=unexpected_shutdowns_7d,json=unexpectedShutdowns7d,proto3" json:"unexpected_shutdowns_7d,omitempty"`
	// «Синих экранов» за 7 суток (журнал System: BugCheck 1001), как unexpected_shutdowns_7d.
	Bugchecks_7D uint32 `protobuf:"varint,11,opt,name=bugchecks_7d,json=bugchecks7d,proto3" json:"bugchecks_7d,omitempty"`
	// Внезапных выключений и «синих экранов» перед ТЕКУЩЕЙ загрузкой Windows: те же события журнала, записанные с начала
	// этой загрузки (их пишет загрузка после сбоя; окно «время работы + 2 мин»). Из окна они не выпадают, пока Windows
	// работает, поэтому сервер считает по ним точный прирост для почасовой истории (вместе с Hello.boot_id), а не по
	// счётчикам за 7 суток (там выпавший из окна старый сбой и новый между двумя Status взаимно гасятся). Есть, только
	// если crashes_boot_measured (иначе — время работы не измерено — сервер не добавляет сбои в почасовую историю).
	UnexpectedShutdownsBoot uint32 `protobuf:"varint,12,opt,name=unexpected_shutdowns_boot,json=unexpectedShutdownsBoot,proto3" json:"unexpected_shutdowns_boot,omitempty"`
	BugchecksBoot           uint32 `protobuf:"varint,13,opt,name=bugchecks_boot,json=bugchecksBoot,proto3" json:"bugchecks_boot,omitempty"`
	CrashesBootMeasured     bool   `protobuf:"varint,14,opt,name=crashes_boot_measured,json=crashesBootMeasured,proto3" json:"crashes_boot_measured,omitempty"`
	unknownFields           protoimpl.UnknownFields
	sizeCache               protoimpl.SizeCache
}

func (x *ComputerHealth) Reset() {
	*x = ComputerHealth{}
	mi := &file_agent_v1_agent_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ComputerHealth) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ComputerHealth) ProtoMessage() {}

func (x *ComputerHealth) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ComputerHealth.ProtoReflect.Descriptor instead.
func (*ComputerHealth) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{4}
}

func (x *ComputerHealth) GetCpuTempDecicelsius() int32 {
	if x != nil {
		return x.CpuTempDecicelsius
	}
	return 0
}

func (x *ComputerHealth) GetGpuTempDecicelsius() int32 {
	if x != nil {
		return x.GpuTempDecicelsius
	}
	return 0
}

func (x *ComputerHealth) GetGpuLoadBasisPoints() uint32 {
	if x != nil {
		return x.GpuLoadBasisPoints
	}
	return 0
}

func (x *ComputerHealth) GetGpuMemoryBasisPoints() uint32 {
	if x != nil {
		return x.GpuMemoryBasisPoints
	}
	return 0
}

func (x *ComputerHealth) GetGpuMeasured() bool {
	if x != nil {
		return x.GpuMeasured
	}
	return false
}

func (x *ComputerHealth) GetDisks() []*DiskHealth {
	if x != nil {
		return x.Disks
	}
	return nil
}

func (x *ComputerHealth) GetPeripherals() []*Peripheral {
	if x != nil {
		return x.Peripherals
	}
	return nil
}

func (x *ComputerHealth) GetLinkSpeedMbps() uint32 {
	if x != nil {
		return x.LinkSpeedMbps
	}
	return 0
}

func (x *ComputerHealth) GetUptimeSeconds() uint64 {
	if x != nil {
		return x.UptimeSeconds
	}
	return 0
}

func (x *ComputerHealth) GetUnexpectedShutdowns_7D() uint32 {
	if x != nil {
		return x.UnexpectedShutdowns_7D
	}
	return 0
}

func (x *ComputerHealth) GetBugchecks_7D() uint32 {
	if x != nil {
		return x.Bugchecks_7D
	}
	return 0
}

func (x *ComputerHealth) GetUnexpectedShutdownsBoot() uint32 {
	if x != nil {
		return x.UnexpectedShutdownsBoot
	}
	return 0
}

func (x *ComputerHealth) GetBugchecksBoot() uint32 {
	if x != nil {
		return x.BugchecksBoot
	}
	return 0
}

func (x *ComputerHealth) GetCrashesBootMeasured() bool {
	if x != nil {
		return x.CrashesBootMeasured
	}
	return false
}

// Локальный диск.
type DiskHealth struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Буква/точка монтирования («C:»).
	Mount string `protobuf:"bytes,1,opt,name=mount,proto3" json:"mount,omitempty"`
	// Системный диск (на нём Windows).
	System bool `protobuf:"varint,2,opt,name=system,proto3" json:"system,omitempty"`
	// Объём и свободное место, МБ.
	TotalMb uint64 `protobuf:"varint,3,opt,name=total_mb,json=totalMb,proto3" json:"total_mb,omitempty"`
	FreeMb  uint64 `protobuf:"varint,4,opt,name=free_mb,json=freeMb,proto3" json:"free_mb,omitempty"`
	// Состояние по SMART/Storage Spaces.
	Smart         DiskSmart `protobuf:"varint,5,opt,name=smart,proto3,enum=zafiro.agent.v1.DiskSmart" json:"smart,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DiskHealth) Reset() {
	*x = DiskHealth{}
	mi := &file_agent_v1_agent_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DiskHealth) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DiskHealth) ProtoMessage() {}

func (x *DiskHealth) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DiskHealth.ProtoReflect.Descriptor instead.
func (*DiskHealth) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{5}
}

func (x *DiskHealth) GetMount() string {
	if x != nil {
		return x.Mount
	}
	return ""
}

func (x *DiskHealth) GetSystem() bool {
	if x != nil {
		return x.System
	}
	return false
}

func (x *DiskHealth) GetTotalMb() uint64 {
	if x != nil {
		return x.TotalMb
	}
	return 0
}

func (x *DiskHealth) GetFreeMb() uint64 {
	if x != nil {
		return x.FreeMb
	}
	return 0
}

func (x *DiskHealth) GetSmart() DiskSmart {
	if x != nil {
		return x.Smart
	}
	return DiskSmart_DISK_SMART_UNSPECIFIED
}

// Периферийное устройство.
type Peripheral struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Вид устройства.
	Kind PeripheralKind `protobuf:"varint,1,opt,name=kind,proto3,enum=zafiro.agent.v1.PeripheralKind" json:"kind,omitempty"`
	// Подключено сейчас.
	Connected bool `protobuf:"varint,2,opt,name=connected,proto3" json:"connected,omitempty"`
	// Название устройства (из Windows; для показа админу), до 128 символов.
	Name          string `protobuf:"bytes,3,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Peripheral) Reset() {
	*x = Peripheral{}
	mi := &file_agent_v1_agent_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Peripheral) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Peripheral) ProtoMessage() {}

func (x *Peripheral) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Peripheral.ProtoReflect.Descriptor instead.
func (*Peripheral) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{6}
}

func (x *Peripheral) GetKind() PeripheralKind {
	if x != nil {
		return x.Kind
	}
	return PeripheralKind_PERIPHERAL_KIND_UNSPECIFIED
}

func (x *Peripheral) GetConnected() bool {
	if x != nil {
		return x.Connected
	}
	return false
}

func (x *Peripheral) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

// Загрузка ПК. Значения в процентах * 100 (0..10000), чтобы не использовать float.
type ComputerLoad struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Загрузка CPU за последний интервал, сотые доли процента.
	CpuBasisPoints uint32 `protobuf:"varint,1,opt,name=cpu_basis_points,json=cpuBasisPoints,proto3" json:"cpu_basis_points,omitempty"`
	// Занятая ОЗУ, сотые доли процента.
	RamBasisPoints uint32 `protobuf:"varint,2,opt,name=ram_basis_points,json=ramBasisPoints,proto3" json:"ram_basis_points,omitempty"`
	// Сколько секунд нет ввода с клавиатуры/мыши (для выхода по бездействию).
	IdleSeconds uint32 `protobuf:"varint,3,opt,name=idle_seconds,json=idleSeconds,proto3" json:"idle_seconds,omitempty"`
	// Процессы агента: суммарная память (рабочий набор), МБ — контроль требования
	// «минимальная нагрузка агента».
	AgentMemoryMb uint32 `protobuf:"varint,4,opt,name=agent_memory_mb,json=agentMemoryMb,proto3" json:"agent_memory_mb,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ComputerLoad) Reset() {
	*x = ComputerLoad{}
	mi := &file_agent_v1_agent_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ComputerLoad) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ComputerLoad) ProtoMessage() {}

func (x *ComputerLoad) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ComputerLoad.ProtoReflect.Descriptor instead.
func (*ComputerLoad) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{7}
}

func (x *ComputerLoad) GetCpuBasisPoints() uint32 {
	if x != nil {
		return x.CpuBasisPoints
	}
	return 0
}

func (x *ComputerLoad) GetRamBasisPoints() uint32 {
	if x != nil {
		return x.RamBasisPoints
	}
	return 0
}

func (x *ComputerLoad) GetIdleSeconds() uint32 {
	if x != nil {
		return x.IdleSeconds
	}
	return 0
}

func (x *ComputerLoad) GetAgentMemoryMb() uint32 {
	if x != nil {
		return x.AgentMemoryMb
	}
	return 0
}

// Событие на ПК. Доставляется «как минимум один раз»: агент хранит неподтверждённые
// события на диске и повторяет их после reconnect, пока не получит EventAck.
type AgentEvent struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// UUID события (дедупликация на сервере).
	EventId string `protobuf:"bytes,1,opt,name=event_id,json=eventId,proto3" json:"event_id,omitempty"`
	// Когда произошло (часы ПК, скорректированные по server_time).
	OccurredAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=occurred_at,json=occurredAt,proto3" json:"occurred_at,omitempty"`
	// Тип события (ровно один).
	//
	// Types that are valid to be assigned to Kind:
	//
	//	*AgentEvent_SessionEnded
	//	*AgentEvent_GuestSignedIn
	//	*AgentEvent_GuestSignedOut
	//	*AgentEvent_Error
	//	*AgentEvent_UpdateResult
	//	*AgentEvent_ServiceModeChanged
	Kind          isAgentEvent_Kind `protobuf_oneof:"kind"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AgentEvent) Reset() {
	*x = AgentEvent{}
	mi := &file_agent_v1_agent_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AgentEvent) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AgentEvent) ProtoMessage() {}

func (x *AgentEvent) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AgentEvent.ProtoReflect.Descriptor instead.
func (*AgentEvent) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{8}
}

func (x *AgentEvent) GetEventId() string {
	if x != nil {
		return x.EventId
	}
	return ""
}

func (x *AgentEvent) GetOccurredAt() *timestamppb.Timestamp {
	if x != nil {
		return x.OccurredAt
	}
	return nil
}

func (x *AgentEvent) GetKind() isAgentEvent_Kind {
	if x != nil {
		return x.Kind
	}
	return nil
}

func (x *AgentEvent) GetSessionEnded() *SessionEnded {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_SessionEnded); ok {
			return x.SessionEnded
		}
	}
	return nil
}

func (x *AgentEvent) GetGuestSignedIn() *GuestSignedIn {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_GuestSignedIn); ok {
			return x.GuestSignedIn
		}
	}
	return nil
}

func (x *AgentEvent) GetGuestSignedOut() *GuestSignedOut {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_GuestSignedOut); ok {
			return x.GuestSignedOut
		}
	}
	return nil
}

func (x *AgentEvent) GetError() *AgentError {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_Error); ok {
			return x.Error
		}
	}
	return nil
}

func (x *AgentEvent) GetUpdateResult() *UpdateResult {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_UpdateResult); ok {
			return x.UpdateResult
		}
	}
	return nil
}

func (x *AgentEvent) GetServiceModeChanged() *ServiceModeChanged {
	if x != nil {
		if x, ok := x.Kind.(*AgentEvent_ServiceModeChanged); ok {
			return x.ServiceModeChanged
		}
	}
	return nil
}

type isAgentEvent_Kind interface {
	isAgentEvent_Kind()
}

type AgentEvent_SessionEnded struct {
	// Сессия закончилась на ПК (время вышло, гость завершил, ПК выключили).
	SessionEnded *SessionEnded `protobuf:"bytes,3,opt,name=session_ended,json=sessionEnded,proto3,oneof"`
}

type AgentEvent_GuestSignedIn struct {
	// Гость вошёл на ПК.
	GuestSignedIn *GuestSignedIn `protobuf:"bytes,4,opt,name=guest_signed_in,json=guestSignedIn,proto3,oneof"`
}

type AgentEvent_GuestSignedOut struct {
	// Гость вышел (сам, по бездействию, по команде).
	GuestSignedOut *GuestSignedOut `protobuf:"bytes,5,opt,name=guest_signed_out,json=guestSignedOut,proto3,oneof"`
}

type AgentEvent_Error struct {
	// Ошибка агента, которую стоит показать в панели/логах.
	Error *AgentError `protobuf:"bytes,6,opt,name=error,proto3,oneof"`
}

type AgentEvent_UpdateResult struct {
	// Результат установки обновления.
	UpdateResult *UpdateResult `protobuf:"bytes,7,opt,name=update_result,json=updateResult,proto3,oneof"`
}

type AgentEvent_ServiceModeChanged struct {
	// Агент закрыт по PIN сотрудника / вернулся из техрежима.
	ServiceModeChanged *ServiceModeChanged `protobuf:"bytes,8,opt,name=service_mode_changed,json=serviceModeChanged,proto3,oneof"`
}

func (*AgentEvent_SessionEnded) isAgentEvent_Kind() {}

func (*AgentEvent_GuestSignedIn) isAgentEvent_Kind() {}

func (*AgentEvent_GuestSignedOut) isAgentEvent_Kind() {}

func (*AgentEvent_Error) isAgentEvent_Kind() {}

func (*AgentEvent_UpdateResult) isAgentEvent_Kind() {}

func (*AgentEvent_ServiceModeChanged) isAgentEvent_Kind() {}

// Событие: сессия закончилась на ПК.
type SessionEnded struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID сессии.
	SessionId string `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	// Причина (как её видит агент).
	Reason        SessionEndReason `protobuf:"varint,2,opt,name=reason,proto3,enum=zafiro.agent.v1.SessionEndReason" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SessionEnded) Reset() {
	*x = SessionEnded{}
	mi := &file_agent_v1_agent_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SessionEnded) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SessionEnded) ProtoMessage() {}

func (x *SessionEnded) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SessionEnded.ProtoReflect.Descriptor instead.
func (*SessionEnded) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{9}
}

func (x *SessionEnded) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *SessionEnded) GetReason() SessionEndReason {
	if x != nil {
		return x.Reason
	}
	return SessionEndReason_SESSION_END_REASON_UNSPECIFIED
}

// Событие: гость вошёл на ПК.
type GuestSignedIn struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID пользователя (0 для гостевой сессии с кассы).
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Способ входа.
	Method        SignInMethod `protobuf:"varint,2,opt,name=method,proto3,enum=zafiro.agent.v1.SignInMethod" json:"method,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GuestSignedIn) Reset() {
	*x = GuestSignedIn{}
	mi := &file_agent_v1_agent_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GuestSignedIn) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GuestSignedIn) ProtoMessage() {}

func (x *GuestSignedIn) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GuestSignedIn.ProtoReflect.Descriptor instead.
func (*GuestSignedIn) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{10}
}

func (x *GuestSignedIn) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *GuestSignedIn) GetMethod() SignInMethod {
	if x != nil {
		return x.Method
	}
	return SignInMethod_SIGN_IN_METHOD_UNSPECIFIED
}

// Событие: гость вышел.
type GuestSignedOut struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID пользователя.
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Причина.
	Reason        SignOutReason `protobuf:"varint,2,opt,name=reason,proto3,enum=zafiro.agent.v1.SignOutReason" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GuestSignedOut) Reset() {
	*x = GuestSignedOut{}
	mi := &file_agent_v1_agent_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GuestSignedOut) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GuestSignedOut) ProtoMessage() {}

func (x *GuestSignedOut) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GuestSignedOut.ProtoReflect.Descriptor instead.
func (*GuestSignedOut) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{11}
}

func (x *GuestSignedOut) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *GuestSignedOut) GetReason() SignOutReason {
	if x != nil {
		return x.Reason
	}
	return SignOutReason_SIGN_OUT_REASON_UNSPECIFIED
}

// Событие: ошибка на ПК.
type AgentError struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Код, если ошибка относится к контракту (иначе UNSPECIFIED).
	ErrorCode ErrorCode `protobuf:"varint,1,opt,name=error_code,json=errorCode,proto3,enum=zafiro.agent.v1.ErrorCode" json:"error_code,omitempty"`
	// Короткий машиночитаемый идентификатор компонента ("webview", "kiosk", "update", ...).
	Component string `protobuf:"bytes,2,opt,name=component,proto3" json:"component,omitempty"`
	// Описание без персональных данных и секретов (≤ 500 символов).
	Message       string `protobuf:"bytes,3,opt,name=message,proto3" json:"message,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AgentError) Reset() {
	*x = AgentError{}
	mi := &file_agent_v1_agent_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AgentError) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AgentError) ProtoMessage() {}

func (x *AgentError) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AgentError.ProtoReflect.Descriptor instead.
func (*AgentError) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{12}
}

func (x *AgentError) GetErrorCode() ErrorCode {
	if x != nil {
		return x.ErrorCode
	}
	return ErrorCode_ERROR_CODE_UNSPECIFIED
}

func (x *AgentError) GetComponent() string {
	if x != nil {
		return x.Component
	}
	return ""
}

func (x *AgentError) GetMessage() string {
	if x != nil {
		return x.Message
	}
	return ""
}

// Событие: результат обновления агента.
type UpdateResult struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Версия, которую пытались установить.
	Version string `protobuf:"bytes,1,opt,name=version,proto3" json:"version,omitempty"`
	// true — установлено и агент перезапущен на новой версии.
	Success bool `protobuf:"varint,2,opt,name=success,proto3" json:"success,omitempty"`
	// Код ошибки при неудаче (например UPDATE_VERIFICATION_FAILED).
	ErrorCode     ErrorCode `protobuf:"varint,3,opt,name=error_code,json=errorCode,proto3,enum=zafiro.agent.v1.ErrorCode" json:"error_code,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateResult) Reset() {
	*x = UpdateResult{}
	mi := &file_agent_v1_agent_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateResult) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateResult) ProtoMessage() {}

func (x *UpdateResult) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateResult.ProtoReflect.Descriptor instead.
func (*UpdateResult) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{13}
}

func (x *UpdateResult) GetVersion() string {
	if x != nil {
		return x.Version
	}
	return ""
}

func (x *UpdateResult) GetSuccess() bool {
	if x != nil {
		return x.Success
	}
	return false
}

func (x *UpdateResult) GetErrorCode() ErrorCode {
	if x != nil {
		return x.ErrorCode
	}
	return ErrorCode_ERROR_CODE_UNSPECIFIED
}

// Событие: агент закрыт сотрудником (техрежим) или вернулся.
type ServiceModeChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// true — техрежим начался, false — закончился.
	Active bool `protobuf:"varint,1,opt,name=active,proto3" json:"active,omitempty"`
	// До какого времени агент закрыт.
	Until         *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=until,proto3" json:"until,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ServiceModeChanged) Reset() {
	*x = ServiceModeChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ServiceModeChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ServiceModeChanged) ProtoMessage() {}

func (x *ServiceModeChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ServiceModeChanged.ProtoReflect.Descriptor instead.
func (*ServiceModeChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{14}
}

func (x *ServiceModeChanged) GetActive() bool {
	if x != nil {
		return x.Active
	}
	return false
}

func (x *ServiceModeChanged) GetUntil() *timestamppb.Timestamp {
	if x != nil {
		return x.Until
	}
	return nil
}

// Ack/результат команды сервера.
type CommandResult struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// command_id из Command.
	CommandId string `protobuf:"bytes,1,opt,name=command_id,json=commandId,proto3" json:"command_id,omitempty"`
	// seq из Command (0 для мгновенных команд).
	Seq uint64 `protobuf:"varint,2,opt,name=seq,proto3" json:"seq,omitempty"`
	// Итог.
	Status CommandStatus `protobuf:"varint,3,opt,name=status,proto3,enum=zafiro.agent.v1.CommandStatus" json:"status,omitempty"`
	// Код ошибки при FAILED.
	ErrorCode ErrorCode `protobuf:"varint,4,opt,name=error_code,json=errorCode,proto3,enum=zafiro.agent.v1.ErrorCode" json:"error_code,omitempty"`
	// Пояснение (≤ 500 символов, без секретов).
	Message string `protobuf:"bytes,5,opt,name=message,proto3" json:"message,omitempty"`
	// Когда выполнена.
	CompletedAt *timestamppb.Timestamp `protobuf:"bytes,6,opt,name=completed_at,json=completedAt,proto3" json:"completed_at,omitempty"`
	// Для Ping: время ПК в момент ответа (оценка рассинхрона часов).
	AgentTime     *timestamppb.Timestamp `protobuf:"bytes,7,opt,name=agent_time,json=agentTime,proto3" json:"agent_time,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CommandResult) Reset() {
	*x = CommandResult{}
	mi := &file_agent_v1_agent_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CommandResult) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CommandResult) ProtoMessage() {}

func (x *CommandResult) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CommandResult.ProtoReflect.Descriptor instead.
func (*CommandResult) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{15}
}

func (x *CommandResult) GetCommandId() string {
	if x != nil {
		return x.CommandId
	}
	return ""
}

func (x *CommandResult) GetSeq() uint64 {
	if x != nil {
		return x.Seq
	}
	return 0
}

func (x *CommandResult) GetStatus() CommandStatus {
	if x != nil {
		return x.Status
	}
	return CommandStatus_COMMAND_STATUS_UNSPECIFIED
}

func (x *CommandResult) GetErrorCode() ErrorCode {
	if x != nil {
		return x.ErrorCode
	}
	return ErrorCode_ERROR_CODE_UNSPECIFIED
}

func (x *CommandResult) GetMessage() string {
	if x != nil {
		return x.Message
	}
	return ""
}

func (x *CommandResult) GetCompletedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.CompletedAt
	}
	return nil
}

func (x *CommandResult) GetAgentTime() *timestamppb.Timestamp {
	if x != nil {
		return x.AgentTime
	}
	return nil
}

// Сообщение сервера в стриме Connect.
type ServerMessage struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Содержимое (ровно одно).
	//
	// Types that are valid to be assigned to Payload:
	//
	//	*ServerMessage_Welcome
	//	*ServerMessage_Command
	//	*ServerMessage_EventAck
	Payload       isServerMessage_Payload `protobuf_oneof:"payload"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ServerMessage) Reset() {
	*x = ServerMessage{}
	mi := &file_agent_v1_agent_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ServerMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ServerMessage) ProtoMessage() {}

func (x *ServerMessage) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ServerMessage.ProtoReflect.Descriptor instead.
func (*ServerMessage) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{16}
}

func (x *ServerMessage) GetPayload() isServerMessage_Payload {
	if x != nil {
		return x.Payload
	}
	return nil
}

func (x *ServerMessage) GetWelcome() *Welcome {
	if x != nil {
		if x, ok := x.Payload.(*ServerMessage_Welcome); ok {
			return x.Welcome
		}
	}
	return nil
}

func (x *ServerMessage) GetCommand() *Command {
	if x != nil {
		if x, ok := x.Payload.(*ServerMessage_Command); ok {
			return x.Command
		}
	}
	return nil
}

func (x *ServerMessage) GetEventAck() *EventAck {
	if x != nil {
		if x, ok := x.Payload.(*ServerMessage_EventAck); ok {
			return x.EventAck
		}
	}
	return nil
}

type isServerMessage_Payload interface {
	isServerMessage_Payload()
}

type ServerMessage_Welcome struct {
	// Ответ на Hello (первое сообщение сервера).
	Welcome *Welcome `protobuf:"bytes,1,opt,name=welcome,proto3,oneof"`
}

type ServerMessage_Command struct {
	// Команда.
	Command *Command `protobuf:"bytes,2,opt,name=command,proto3,oneof"`
}

type ServerMessage_EventAck struct {
	// Подтверждение события агента.
	EventAck *EventAck `protobuf:"bytes,3,opt,name=event_ack,json=eventAck,proto3,oneof"`
}

func (*ServerMessage_Welcome) isServerMessage_Payload() {}

func (*ServerMessage_Command) isServerMessage_Payload() {}

func (*ServerMessage_EventAck) isServerMessage_Payload() {}

// Ответ на Hello: полный снимок, после которого агенту не нужны отдельные запросы.
type Welcome struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID ПК (из сертификата).
	ComputerId int64 `protobuf:"varint,1,opt,name=computer_id,json=computerId,proto3" json:"computer_id,omitempty"`
	// ID клуба.
	ClubId int64 `protobuf:"varint,2,opt,name=club_id,json=clubId,proto3" json:"club_id,omitempty"`
	// Номер ПК и зона.
	Computer *ComputerRef `protobuf:"bytes,3,opt,name=computer,proto3" json:"computer,omitempty"`
	// Время сервера (агент считает конец сессии по нему).
	ServerTime *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=server_time,json=serverTime,proto3" json:"server_time,omitempty"`
	// Как часто слать Status без изменений.
	StatusInterval *durationpb.Duration `protobuf:"bytes,5,opt,name=status_interval,json=statusInterval,proto3" json:"status_interval,omitempty"`
	// Настройки клуба для агента.
	Config *ClubConfig `protobuf:"bytes,6,opt,name=config,proto3" json:"config,omitempty"`
	// Активная сессия ПК (нет — сессии нет).
	Session *ActiveSession `protobuf:"bytes,7,opt,name=session,proto3" json:"session,omitempty"`
	// Актуальная версия переводов агента (TranslationCatalog::version в Laravel).
	TranslationsVersion int64 `protobuf:"varint,8,opt,name=translations_version,json=translationsVersion,proto3" json:"translations_version,omitempty"`
	// Поддерживаемые сервером версии протокола.
	MinProtocolVersion uint32 `protobuf:"varint,9,opt,name=min_protocol_version,json=minProtocolVersion,proto3" json:"min_protocol_version,omitempty"`
	// Максимальная поддерживаемая версия протокола.
	MaxProtocolVersion uint32 `protobuf:"varint,10,opt,name=max_protocol_version,json=maxProtocolVersion,proto3" json:"max_protocol_version,omitempty"`
	// Последний seq, который сервер уже выдал этому ПК (агент может понять, сколько команд
	// будет дослано).
	LatestCommandSeq uint64 `protobuf:"varint,11,opt,name=latest_command_seq,json=latestCommandSeq,proto3" json:"latest_command_seq,omitempty"`
	// Когда продлевать сертификат (дублирует EnrollResponse.renew_after; если сервер решил
	// перевыпустить раньше, например после смены CA).
	CertificateRenewAfter *timestamppb.Timestamp `protobuf:"bytes,12,opt,name=certificate_renew_after,json=certificateRenewAfter,proto3" json:"certificate_renew_after,omitempty"`
	unknownFields         protoimpl.UnknownFields
	sizeCache             protoimpl.SizeCache
}

func (x *Welcome) Reset() {
	*x = Welcome{}
	mi := &file_agent_v1_agent_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Welcome) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Welcome) ProtoMessage() {}

func (x *Welcome) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Welcome.ProtoReflect.Descriptor instead.
func (*Welcome) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{17}
}

func (x *Welcome) GetComputerId() int64 {
	if x != nil {
		return x.ComputerId
	}
	return 0
}

func (x *Welcome) GetClubId() int64 {
	if x != nil {
		return x.ClubId
	}
	return 0
}

func (x *Welcome) GetComputer() *ComputerRef {
	if x != nil {
		return x.Computer
	}
	return nil
}

func (x *Welcome) GetServerTime() *timestamppb.Timestamp {
	if x != nil {
		return x.ServerTime
	}
	return nil
}

func (x *Welcome) GetStatusInterval() *durationpb.Duration {
	if x != nil {
		return x.StatusInterval
	}
	return nil
}

func (x *Welcome) GetConfig() *ClubConfig {
	if x != nil {
		return x.Config
	}
	return nil
}

func (x *Welcome) GetSession() *ActiveSession {
	if x != nil {
		return x.Session
	}
	return nil
}

func (x *Welcome) GetTranslationsVersion() int64 {
	if x != nil {
		return x.TranslationsVersion
	}
	return 0
}

func (x *Welcome) GetMinProtocolVersion() uint32 {
	if x != nil {
		return x.MinProtocolVersion
	}
	return 0
}

func (x *Welcome) GetMaxProtocolVersion() uint32 {
	if x != nil {
		return x.MaxProtocolVersion
	}
	return 0
}

func (x *Welcome) GetLatestCommandSeq() uint64 {
	if x != nil {
		return x.LatestCommandSeq
	}
	return 0
}

func (x *Welcome) GetCertificateRenewAfter() *timestamppb.Timestamp {
	if x != nil {
		return x.CertificateRenewAfter
	}
	return nil
}

// Возможности гостя, включаемые в настройках клуба «Гости».
// Старый аналог: GuestFeatures::SETTINGS + shopEnabled() (app/Services/GuestFeatures.php).
type GuestFeatures struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// QR-вход с телефона (show_qr_code_on_login).
	QrLogin bool `protobuf:"varint,1,opt,name=qr_login,json=qrLogin,proto3" json:"qr_login,omitempty"`
	// Пересадка на другой ПК самим гостем (allow_guest_self_transfer).
	SelfTransfer bool `protobuf:"varint,2,opt,name=self_transfer,json=selfTransfer,proto3" json:"self_transfer,omitempty"`
	// Друзья (allow_adding_friends).
	Friends bool `protobuf:"varint,3,opt,name=friends,proto3" json:"friends,omitempty"`
	// Перевод баланса друзьям (allow_balance_transfer_to_friends).
	BalanceTransfer bool `protobuf:"varint,4,opt,name=balance_transfer,json=balanceTransfer,proto3" json:"balance_transfer,omitempty"`
	// Магазин в агенте.
	Shop          bool `protobuf:"varint,5,opt,name=shop,proto3" json:"shop,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GuestFeatures) Reset() {
	*x = GuestFeatures{}
	mi := &file_agent_v1_agent_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GuestFeatures) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GuestFeatures) ProtoMessage() {}

func (x *GuestFeatures) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GuestFeatures.ProtoReflect.Descriptor instead.
func (*GuestFeatures) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{18}
}

func (x *GuestFeatures) GetQrLogin() bool {
	if x != nil {
		return x.QrLogin
	}
	return false
}

func (x *GuestFeatures) GetSelfTransfer() bool {
	if x != nil {
		return x.SelfTransfer
	}
	return false
}

func (x *GuestFeatures) GetFriends() bool {
	if x != nil {
		return x.Friends
	}
	return false
}

func (x *GuestFeatures) GetBalanceTransfer() bool {
	if x != nil {
		return x.BalanceTransfer
	}
	return false
}

func (x *GuestFeatures) GetShop() bool {
	if x != nil {
		return x.Shop
	}
	return false
}

// Язык интерфейса, включённый на платформе.
type Language struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Код (ru, kk, en, ...).
	Code string `protobuf:"bytes,1,opt,name=code,proto3" json:"code,omitempty"`
	// Название на самом языке ("Қазақша").
	Name          string `protobuf:"bytes,2,opt,name=name,proto3" json:"name,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Language) Reset() {
	*x = Language{}
	mi := &file_agent_v1_agent_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Language) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Language) ProtoMessage() {}

func (x *Language) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Language.ProtoReflect.Descriptor instead.
func (*Language) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{19}
}

func (x *Language) GetCode() string {
	if x != nil {
		return x.Code
	}
	return ""
}

func (x *Language) GetName() string {
	if x != nil {
		return x.Name
	}
	return ""
}

// Настройки клуба для агента. Старый аналог: блок `club` в ответе heartbeat
// (ClubComputerController::heartbeatResponse).
type ClubConfig struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Название клуба.
	ClubName string `protobuf:"bytes,1,opt,name=club_name,json=clubName,proto3" json:"club_name,omitempty"`
	// Логотип (HTTPS URL) или пусто.
	LogoUrl string `protobuf:"bytes,2,opt,name=logo_url,json=logoUrl,proto3" json:"logo_url,omitempty"`
	// Картинка экрана входа (HTTPS URL) или пусто — стандартная.
	SigninImageUrl string `protobuf:"bytes,3,opt,name=signin_image_url,json=signinImageUrl,proto3" json:"signin_image_url,omitempty"`
	// Язык клуба по умолчанию.
	DefaultLocale string `protobuf:"bytes,4,opt,name=default_locale,json=defaultLocale,proto3" json:"default_locale,omitempty"`
	// Доступные языки.
	Languages []*Language `protobuf:"bytes,5,rep,name=languages,proto3" json:"languages,omitempty"`
	// Что делать после окончания сессии.
	AfterSessionAction AfterSessionAction `protobuf:"varint,6,opt,name=after_session_action,json=afterSessionAction,proto3,enum=zafiro.agent.v1.AfterSessionAction" json:"after_session_action,omitempty"`
	// Через сколько выполнить after_session_action, если гость не купил новое время.
	AfterSessionDelay *durationpb.Duration `protobuf:"bytes,7,opt,name=after_session_delay,json=afterSessionDelay,proto3" json:"after_session_delay,omitempty"`
	// Возможности гостя.
	GuestFeatures *GuestFeatures `protobuf:"bytes,8,opt,name=guest_features,json=guestFeatures,proto3" json:"guest_features,omitempty"`
	// Выход гостя без оплаченной сессии через столько минут бездействия (0 — не выходить).
	// По умолчанию 5 (GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES).
	IdleLogoutMinutes uint32 `protobuf:"varint,9,opt,name=idle_logout_minutes,json=idleLogoutMinutes,proto3" json:"idle_logout_minutes,omitempty"`
	// Валюта клуба (finances.currency, по умолчанию KZT).
	Currency string `protobuf:"bytes,10,opt,name=currency,proto3" json:"currency,omitempty"`
	// Сколько минут длится техрежим сотрудника (clubs.agent_staff_minutes, по умолчанию 15 —
	// ClubComputerSessions::DEFAULT_STAFF_MINUTES).
	StaffMinutes uint32 `protobuf:"varint,11,opt,name=staff_minutes,json=staffMinutes,proto3" json:"staff_minutes,omitempty"`
	// Автобиллинг (поминутная оплата стартует сама) — настройка finances.auto_billing.
	AutoBilling bool `protobuf:"varint,12,opt,name=auto_billing,json=autoBilling,proto3" json:"auto_billing,omitempty"`
	// Пауза после входа до старта автобиллинга, минуты (PaymentRules::autoBillingPauseMinutes).
	AutoBillingPauseMinutes uint32 `protobuf:"varint,13,opt,name=auto_billing_pause_minutes,json=autoBillingPauseMinutes,proto3" json:"auto_billing_pause_minutes,omitempty"`
	unknownFields           protoimpl.UnknownFields
	sizeCache               protoimpl.SizeCache
}

func (x *ClubConfig) Reset() {
	*x = ClubConfig{}
	mi := &file_agent_v1_agent_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ClubConfig) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ClubConfig) ProtoMessage() {}

func (x *ClubConfig) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ClubConfig.ProtoReflect.Descriptor instead.
func (*ClubConfig) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{20}
}

func (x *ClubConfig) GetClubName() string {
	if x != nil {
		return x.ClubName
	}
	return ""
}

func (x *ClubConfig) GetLogoUrl() string {
	if x != nil {
		return x.LogoUrl
	}
	return ""
}

func (x *ClubConfig) GetSigninImageUrl() string {
	if x != nil {
		return x.SigninImageUrl
	}
	return ""
}

func (x *ClubConfig) GetDefaultLocale() string {
	if x != nil {
		return x.DefaultLocale
	}
	return ""
}

func (x *ClubConfig) GetLanguages() []*Language {
	if x != nil {
		return x.Languages
	}
	return nil
}

func (x *ClubConfig) GetAfterSessionAction() AfterSessionAction {
	if x != nil {
		return x.AfterSessionAction
	}
	return AfterSessionAction_AFTER_SESSION_ACTION_UNSPECIFIED
}

func (x *ClubConfig) GetAfterSessionDelay() *durationpb.Duration {
	if x != nil {
		return x.AfterSessionDelay
	}
	return nil
}

func (x *ClubConfig) GetGuestFeatures() *GuestFeatures {
	if x != nil {
		return x.GuestFeatures
	}
	return nil
}

func (x *ClubConfig) GetIdleLogoutMinutes() uint32 {
	if x != nil {
		return x.IdleLogoutMinutes
	}
	return 0
}

func (x *ClubConfig) GetCurrency() string {
	if x != nil {
		return x.Currency
	}
	return ""
}

func (x *ClubConfig) GetStaffMinutes() uint32 {
	if x != nil {
		return x.StaffMinutes
	}
	return 0
}

func (x *ClubConfig) GetAutoBilling() bool {
	if x != nil {
		return x.AutoBilling
	}
	return false
}

func (x *ClubConfig) GetAutoBillingPauseMinutes() uint32 {
	if x != nil {
		return x.AutoBillingPauseMinutes
	}
	return 0
}

// Подтверждение события агента.
type EventAck struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// event_id подтверждённого события.
	EventId       string `protobuf:"bytes,1,opt,name=event_id,json=eventId,proto3" json:"event_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EventAck) Reset() {
	*x = EventAck{}
	mi := &file_agent_v1_agent_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EventAck) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EventAck) ProtoMessage() {}

func (x *EventAck) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EventAck.ProtoReflect.Descriptor instead.
func (*EventAck) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{21}
}

func (x *EventAck) GetEventId() string {
	if x != nil {
		return x.EventId
	}
	return ""
}

// Команда сервера.
type Command struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// UUID v7 команды (идемпотентность, см. шапку п. 5).
	CommandId string `protobuf:"bytes,1,opt,name=command_id,json=commandId,proto3" json:"command_id,omitempty"`
	// Номер долговременной команды в рамках ПК (> 0) или 0 для мгновенной (шапка п. 6).
	Seq uint64 `protobuf:"varint,2,opt,name=seq,proto3" json:"seq,omitempty"`
	// Когда создана.
	IssuedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=issued_at,json=issuedAt,proto3" json:"issued_at,omitempty"`
	// После этого момента не выполнять (ответ FAILED + COMMAND_EXPIRED). Пусто — без срока.
	// Старые TTL: команды питания 120 с (ComputerPower::COMMAND_TTL_SECONDS),
	// сообщения 300 с (ComputerPower::MESSAGE_TTL_SECONDS).
	ExpiresAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=expires_at,json=expiresAt,proto3" json:"expires_at,omitempty"`
	// Кто инициировал (для журнала на ПК): user_id сотрудника или 0 — система.
	IssuedByUserId int64 `protobuf:"varint,5,opt,name=issued_by_user_id,json=issuedByUserId,proto3" json:"issued_by_user_id,omitempty"`
	// Сама команда (ровно одна).
	//
	// Types that are valid to be assigned to Kind:
	//
	//	*Command_StartSession
	//	*Command_EndSession
	//	*Command_Lock
	//	*Command_Unlock
	//	*Command_Reboot
	//	*Command_Shutdown
	//	*Command_ShowMessage
	//	*Command_BalanceChanged
	//	*Command_UpdateAvailable
	//	*Command_TranslationsChanged
	//	*Command_ConfigChanged
	//	*Command_Ping
	//	*Command_EnterServiceMode
	//	*Command_WakeOnLan
	//	*Command_SignOutGuest
	//	*Command_GuestCountersChanged
	//	*Command_ChatMessageReceived
	//	*Command_QrLoginApproved
	//	*Command_Drain
	//	*Command_AutoBillingChanged
	Kind          isCommand_Kind `protobuf_oneof:"kind"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Command) Reset() {
	*x = Command{}
	mi := &file_agent_v1_agent_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Command) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Command) ProtoMessage() {}

func (x *Command) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Command.ProtoReflect.Descriptor instead.
func (*Command) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{22}
}

func (x *Command) GetCommandId() string {
	if x != nil {
		return x.CommandId
	}
	return ""
}

func (x *Command) GetSeq() uint64 {
	if x != nil {
		return x.Seq
	}
	return 0
}

func (x *Command) GetIssuedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.IssuedAt
	}
	return nil
}

func (x *Command) GetExpiresAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ExpiresAt
	}
	return nil
}

func (x *Command) GetIssuedByUserId() int64 {
	if x != nil {
		return x.IssuedByUserId
	}
	return 0
}

func (x *Command) GetKind() isCommand_Kind {
	if x != nil {
		return x.Kind
	}
	return nil
}

func (x *Command) GetStartSession() *StartSession {
	if x != nil {
		if x, ok := x.Kind.(*Command_StartSession); ok {
			return x.StartSession
		}
	}
	return nil
}

func (x *Command) GetEndSession() *EndSession {
	if x != nil {
		if x, ok := x.Kind.(*Command_EndSession); ok {
			return x.EndSession
		}
	}
	return nil
}

func (x *Command) GetLock() *Lock {
	if x != nil {
		if x, ok := x.Kind.(*Command_Lock); ok {
			return x.Lock
		}
	}
	return nil
}

func (x *Command) GetUnlock() *Unlock {
	if x != nil {
		if x, ok := x.Kind.(*Command_Unlock); ok {
			return x.Unlock
		}
	}
	return nil
}

func (x *Command) GetReboot() *Reboot {
	if x != nil {
		if x, ok := x.Kind.(*Command_Reboot); ok {
			return x.Reboot
		}
	}
	return nil
}

func (x *Command) GetShutdown() *Shutdown {
	if x != nil {
		if x, ok := x.Kind.(*Command_Shutdown); ok {
			return x.Shutdown
		}
	}
	return nil
}

func (x *Command) GetShowMessage() *ShowMessage {
	if x != nil {
		if x, ok := x.Kind.(*Command_ShowMessage); ok {
			return x.ShowMessage
		}
	}
	return nil
}

func (x *Command) GetBalanceChanged() *BalanceChanged {
	if x != nil {
		if x, ok := x.Kind.(*Command_BalanceChanged); ok {
			return x.BalanceChanged
		}
	}
	return nil
}

func (x *Command) GetUpdateAvailable() *UpdateAvailable {
	if x != nil {
		if x, ok := x.Kind.(*Command_UpdateAvailable); ok {
			return x.UpdateAvailable
		}
	}
	return nil
}

func (x *Command) GetTranslationsChanged() *TranslationsChanged {
	if x != nil {
		if x, ok := x.Kind.(*Command_TranslationsChanged); ok {
			return x.TranslationsChanged
		}
	}
	return nil
}

func (x *Command) GetConfigChanged() *ConfigChanged {
	if x != nil {
		if x, ok := x.Kind.(*Command_ConfigChanged); ok {
			return x.ConfigChanged
		}
	}
	return nil
}

func (x *Command) GetPing() *Ping {
	if x != nil {
		if x, ok := x.Kind.(*Command_Ping); ok {
			return x.Ping
		}
	}
	return nil
}

func (x *Command) GetEnterServiceMode() *EnterServiceMode {
	if x != nil {
		if x, ok := x.Kind.(*Command_EnterServiceMode); ok {
			return x.EnterServiceMode
		}
	}
	return nil
}

func (x *Command) GetWakeOnLan() *WakeOnLan {
	if x != nil {
		if x, ok := x.Kind.(*Command_WakeOnLan); ok {
			return x.WakeOnLan
		}
	}
	return nil
}

func (x *Command) GetSignOutGuest() *SignOutGuest {
	if x != nil {
		if x, ok := x.Kind.(*Command_SignOutGuest); ok {
			return x.SignOutGuest
		}
	}
	return nil
}

func (x *Command) GetGuestCountersChanged() *GuestCountersChanged {
	if x != nil {
		if x, ok := x.Kind.(*Command_GuestCountersChanged); ok {
			return x.GuestCountersChanged
		}
	}
	return nil
}

func (x *Command) GetChatMessageReceived() *ChatMessageReceived {
	if x != nil {
		if x, ok := x.Kind.(*Command_ChatMessageReceived); ok {
			return x.ChatMessageReceived
		}
	}
	return nil
}

func (x *Command) GetQrLoginApproved() *QrLoginApproved {
	if x != nil {
		if x, ok := x.Kind.(*Command_QrLoginApproved); ok {
			return x.QrLoginApproved
		}
	}
	return nil
}

func (x *Command) GetDrain() *Drain {
	if x != nil {
		if x, ok := x.Kind.(*Command_Drain); ok {
			return x.Drain
		}
	}
	return nil
}

func (x *Command) GetAutoBillingChanged() *AutoBillingChanged {
	if x != nil {
		if x, ok := x.Kind.(*Command_AutoBillingChanged); ok {
			return x.AutoBillingChanged
		}
	}
	return nil
}

type isCommand_Kind interface {
	isCommand_Kind()
}

type Command_StartSession struct {
	// Начать (или продлить) оплаченную сессию.
	StartSession *StartSession `protobuf:"bytes,10,opt,name=start_session,json=startSession,proto3,oneof"`
}

type Command_EndSession struct {
	// Завершить сессию.
	EndSession *EndSession `protobuf:"bytes,11,opt,name=end_session,json=endSession,proto3,oneof"`
}

type Command_Lock struct {
	// Заблокировать ПК (экран входа/блокировки).
	Lock *Lock `protobuf:"bytes,12,opt,name=lock,proto3,oneof"`
}

type Command_Unlock struct {
	// Разблокировать ПК.
	Unlock *Unlock `protobuf:"bytes,13,opt,name=unlock,proto3,oneof"`
}

type Command_Reboot struct {
	// Перезагрузить ПК.
	Reboot *Reboot `protobuf:"bytes,14,opt,name=reboot,proto3,oneof"`
}

type Command_Shutdown struct {
	// Выключить ПК.
	Shutdown *Shutdown `protobuf:"bytes,15,opt,name=shutdown,proto3,oneof"`
}

type Command_ShowMessage struct {
	// Показать сообщение сотрудника.
	ShowMessage *ShowMessage `protobuf:"bytes,16,opt,name=show_message,json=showMessage,proto3,oneof"`
}

type Command_BalanceChanged struct {
	// Изменился баланс вошедшего гостя.
	BalanceChanged *BalanceChanged `protobuf:"bytes,17,opt,name=balance_changed,json=balanceChanged,proto3,oneof"`
}

type Command_UpdateAvailable struct {
	// Доступно обновление агента.
	UpdateAvailable *UpdateAvailable `protobuf:"bytes,18,opt,name=update_available,json=updateAvailable,proto3,oneof"`
}

type Command_TranslationsChanged struct {
	// Изменились переводы.
	TranslationsChanged *TranslationsChanged `protobuf:"bytes,19,opt,name=translations_changed,json=translationsChanged,proto3,oneof"`
}

type Command_ConfigChanged struct {
	// Изменились настройки клуба.
	ConfigChanged *ConfigChanged `protobuf:"bytes,20,opt,name=config_changed,json=configChanged,proto3,oneof"`
}

type Command_Ping struct {
	// Проверка связи / синхронизация часов.
	Ping *Ping `protobuf:"bytes,21,opt,name=ping,proto3,oneof"`
}

type Command_EnterServiceMode struct {
	// Техрежим сотрудника (закрыть агент на staff_minutes).
	EnterServiceMode *EnterServiceMode `protobuf:"bytes,22,opt,name=enter_service_mode,json=enterServiceMode,proto3,oneof"`
}

type Command_WakeOnLan struct {
	// Разбудить другие ПК клуба (Wake-on-LAN).
	WakeOnLan *WakeOnLan `protobuf:"bytes,23,opt,name=wake_on_lan,json=wakeOnLan,proto3,oneof"`
}

type Command_SignOutGuest struct {
	// Выйти из аккаунта гостя.
	SignOutGuest *SignOutGuest `protobuf:"bytes,24,opt,name=sign_out_guest,json=signOutGuest,proto3,oneof"`
}

type Command_GuestCountersChanged struct {
	// Изменились счётчики гостя (заявки в друзья, чат).
	GuestCountersChanged *GuestCountersChanged `protobuf:"bytes,25,opt,name=guest_counters_changed,json=guestCountersChanged,proto3,oneof"`
}

type Command_ChatMessageReceived struct {
	// Новое сообщение чата от сотрудника.
	ChatMessageReceived *ChatMessageReceived `protobuf:"bytes,26,opt,name=chat_message_received,json=chatMessageReceived,proto3,oneof"`
}

type Command_QrLoginApproved struct {
	// QR-вход подтверждён на телефоне (подсказка: агент сразу вызывает WatchQrLogin/получает
	// токен; сам токен по стриму НЕ передаётся).
	QrLoginApproved *QrLoginApproved `protobuf:"bytes,27,opt,name=qr_login_approved,json=qrLoginApproved,proto3,oneof"`
}

type Command_Drain struct {
	// Сервер просит переподключиться (плановый перезапуск).
	Drain *Drain `protobuf:"bytes,28,opt,name=drain,proto3,oneof"`
}

type Command_AutoBillingChanged struct {
	// Изменилось состояние автобиллинга гостя (мгновенная, seq = 0).
	AutoBillingChanged *AutoBillingChanged `protobuf:"bytes,29,opt,name=auto_billing_changed,json=autoBillingChanged,proto3,oneof"`
}

func (*Command_StartSession) isCommand_Kind() {}

func (*Command_EndSession) isCommand_Kind() {}

func (*Command_Lock) isCommand_Kind() {}

func (*Command_Unlock) isCommand_Kind() {}

func (*Command_Reboot) isCommand_Kind() {}

func (*Command_Shutdown) isCommand_Kind() {}

func (*Command_ShowMessage) isCommand_Kind() {}

func (*Command_BalanceChanged) isCommand_Kind() {}

func (*Command_UpdateAvailable) isCommand_Kind() {}

func (*Command_TranslationsChanged) isCommand_Kind() {}

func (*Command_ConfigChanged) isCommand_Kind() {}

func (*Command_Ping) isCommand_Kind() {}

func (*Command_EnterServiceMode) isCommand_Kind() {}

func (*Command_WakeOnLan) isCommand_Kind() {}

func (*Command_SignOutGuest) isCommand_Kind() {}

func (*Command_GuestCountersChanged) isCommand_Kind() {}

func (*Command_ChatMessageReceived) isCommand_Kind() {}

func (*Command_QrLoginApproved) isCommand_Kind() {}

func (*Command_Drain) isCommand_Kind() {}

func (*Command_AutoBillingChanged) isCommand_Kind() {}

// Автобиллинг гостя изменился: началась пауза, остановлен, после покупки ждёт конца сессии.
// Мгновенная; после переподключения агент читает состояние GuestService.GetAutoBilling.
type AutoBillingChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID гостя, к которому относится (агент игнорирует, если вошёл другой).
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Состояние.
	Status        *AutoBillingStatus `protobuf:"bytes,2,opt,name=status,proto3" json:"status,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AutoBillingChanged) Reset() {
	*x = AutoBillingChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AutoBillingChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AutoBillingChanged) ProtoMessage() {}

func (x *AutoBillingChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AutoBillingChanged.ProtoReflect.Descriptor instead.
func (*AutoBillingChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{23}
}

func (x *AutoBillingChanged) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *AutoBillingChanged) GetStatus() *AutoBillingStatus {
	if x != nil {
		return x.Status
	}
	return nil
}

// Начать или продлить сессию (оплата на кассе, в панели, покупка с телефона, пересадка).
// Долговременная. Старый аналог: поле session в ответе heartbeat.
type StartSession struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Сессия целиком (если session_id совпадает с текущей — это продление: новый ends_at).
	Session       *ActiveSession `protobuf:"bytes,1,opt,name=session,proto3" json:"session,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *StartSession) Reset() {
	*x = StartSession{}
	mi := &file_agent_v1_agent_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *StartSession) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StartSession) ProtoMessage() {}

func (x *StartSession) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StartSession.ProtoReflect.Descriptor instead.
func (*StartSession) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{24}
}

func (x *StartSession) GetSession() *ActiveSession {
	if x != nil {
		return x.Session
	}
	return nil
}

// Завершить сессию. Долговременная.
type EndSession struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Какую сессию завершить (если на ПК другая — FAILED + SESSION_MISMATCH).
	SessionId string `protobuf:"bytes,1,opt,name=session_id,json=sessionId,proto3" json:"session_id,omitempty"`
	// Причина (для текста на экране).
	Reason SessionEndReason `protobuf:"varint,2,opt,name=reason,proto3,enum=zafiro.agent.v1.SessionEndReason" json:"reason,omitempty"`
	// Возвращено на основной баланс, тиыны (если завершили с возвратом).
	RefundedMainTiyn int64 `protobuf:"varint,3,opt,name=refunded_main_tiyn,json=refundedMainTiyn,proto3" json:"refunded_main_tiyn,omitempty"`
	// Возвращено на бонусный баланс, тиыны.
	RefundedBonusTiyn int64 `protobuf:"varint,4,opt,name=refunded_bonus_tiyn,json=refundedBonusTiyn,proto3" json:"refunded_bonus_tiyn,omitempty"`
	// Минут сохранено пакетом (TimePackage).
	KeptMinutes   int32 `protobuf:"varint,5,opt,name=kept_minutes,json=keptMinutes,proto3" json:"kept_minutes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSession) Reset() {
	*x = EndSession{}
	mi := &file_agent_v1_agent_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSession) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSession) ProtoMessage() {}

func (x *EndSession) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSession.ProtoReflect.Descriptor instead.
func (*EndSession) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{25}
}

func (x *EndSession) GetSessionId() string {
	if x != nil {
		return x.SessionId
	}
	return ""
}

func (x *EndSession) GetReason() SessionEndReason {
	if x != nil {
		return x.Reason
	}
	return SessionEndReason_SESSION_END_REASON_UNSPECIFIED
}

func (x *EndSession) GetRefundedMainTiyn() int64 {
	if x != nil {
		return x.RefundedMainTiyn
	}
	return 0
}

func (x *EndSession) GetRefundedBonusTiyn() int64 {
	if x != nil {
		return x.RefundedBonusTiyn
	}
	return 0
}

func (x *EndSession) GetKeptMinutes() int32 {
	if x != nil {
		return x.KeptMinutes
	}
	return 0
}

// Заблокировать ПК. Долговременная.
type Lock struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Текст на экране блокировки (пусто — стандартный).
	Reason        string `protobuf:"bytes,1,opt,name=reason,proto3" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Lock) Reset() {
	*x = Lock{}
	mi := &file_agent_v1_agent_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Lock) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Lock) ProtoMessage() {}

func (x *Lock) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Lock.ProtoReflect.Descriptor instead.
func (*Lock) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{26}
}

func (x *Lock) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

// Разблокировать ПК. Долговременная.
type Unlock struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Unlock) Reset() {
	*x = Unlock{}
	mi := &file_agent_v1_agent_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Unlock) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Unlock) ProtoMessage() {}

func (x *Unlock) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Unlock.ProtoReflect.Descriptor instead.
func (*Unlock) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{27}
}

// Перезагрузка. Долговременная. Старый аналог: power_command = "restart".
type Reboot struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Задержка перед перезагрузкой (агент показывает обратный отсчёт).
	Delay         *durationpb.Duration `protobuf:"bytes,1,opt,name=delay,proto3" json:"delay,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Reboot) Reset() {
	*x = Reboot{}
	mi := &file_agent_v1_agent_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Reboot) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Reboot) ProtoMessage() {}

func (x *Reboot) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Reboot.ProtoReflect.Descriptor instead.
func (*Reboot) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{28}
}

func (x *Reboot) GetDelay() *durationpb.Duration {
	if x != nil {
		return x.Delay
	}
	return nil
}

// Выключение. Долговременная. Старый аналог: power_command = "shutdown".
type Shutdown struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Задержка перед выключением.
	Delay         *durationpb.Duration `protobuf:"bytes,1,opt,name=delay,proto3" json:"delay,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Shutdown) Reset() {
	*x = Shutdown{}
	mi := &file_agent_v1_agent_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Shutdown) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Shutdown) ProtoMessage() {}

func (x *Shutdown) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Shutdown.ProtoReflect.Descriptor instead.
func (*Shutdown) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{29}
}

func (x *Shutdown) GetDelay() *durationpb.Duration {
	if x != nil {
		return x.Delay
	}
	return nil
}

// Техрежим (агент закрывается, служба возвращает его через staff_minutes).
// Долговременная. Старый аналог: power_command = "service" + staff_minutes.
type EnterServiceMode struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// На сколько минут закрыть агент.
	StaffMinutes  uint32 `protobuf:"varint,1,opt,name=staff_minutes,json=staffMinutes,proto3" json:"staff_minutes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EnterServiceMode) Reset() {
	*x = EnterServiceMode{}
	mi := &file_agent_v1_agent_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EnterServiceMode) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EnterServiceMode) ProtoMessage() {}

func (x *EnterServiceMode) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EnterServiceMode.ProtoReflect.Descriptor instead.
func (*EnterServiceMode) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{30}
}

func (x *EnterServiceMode) GetStaffMinutes() uint32 {
	if x != nil {
		return x.StaffMinutes
	}
	return 0
}

// Сообщение сотрудника в углу экрана. Долговременная (с expires_at).
// Старый аналог: staff_message (≤ 300 символов, StaffCommands.MaxMessageLength).
type ShowMessage struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Текст (≤ 300 символов; агент обрезает длиннее).
	Text string `protobuf:"bytes,1,opt,name=text,proto3" json:"text,omitempty"`
	// Сколько показывать (пусто — пока не закроют).
	DisplayFor    *durationpb.Duration `protobuf:"bytes,2,opt,name=display_for,json=displayFor,proto3" json:"display_for,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ShowMessage) Reset() {
	*x = ShowMessage{}
	mi := &file_agent_v1_agent_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ShowMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ShowMessage) ProtoMessage() {}

func (x *ShowMessage) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ShowMessage.ProtoReflect.Descriptor instead.
func (*ShowMessage) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{31}
}

func (x *ShowMessage) GetText() string {
	if x != nil {
		return x.Text
	}
	return ""
}

func (x *ShowMessage) GetDisplayFor() *durationpb.Duration {
	if x != nil {
		return x.DisplayFor
	}
	return nil
}

// Баланс вошедшего гостя изменился (пополнение на кассе, перевод, возврат). Мгновенная.
// Заменяет опрос GET /api/me каждые 20 с (MainWindow.BalanceRefreshInterval) и
// customer.balance в ответе heartbeat.
type BalanceChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Чей баланс (агент игнорирует, если вошёл другой гость).
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Новые балансы.
	Balance       *Balance `protobuf:"bytes,2,opt,name=balance,proto3" json:"balance,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *BalanceChanged) Reset() {
	*x = BalanceChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[32]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *BalanceChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*BalanceChanged) ProtoMessage() {}

func (x *BalanceChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[32]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use BalanceChanged.ProtoReflect.Descriptor instead.
func (*BalanceChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{32}
}

func (x *BalanceChanged) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *BalanceChanged) GetBalance() *Balance {
	if x != nil {
		return x.Balance
	}
	return nil
}

// Счётчики гостя. Мгновенная. Старый аналог: customer.friend_requests / chat_last_staff_id.
type GuestCountersChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Чьи счётчики.
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Входящие заявки в друзья.
	PendingFriendRequests uint32 `protobuf:"varint,2,opt,name=pending_friend_requests,json=pendingFriendRequests,proto3" json:"pending_friend_requests,omitempty"`
	// ID последнего сообщения сотрудника в чате.
	LastStaffChatMessageId int64 `protobuf:"varint,3,opt,name=last_staff_chat_message_id,json=lastStaffChatMessageId,proto3" json:"last_staff_chat_message_id,omitempty"`
	unknownFields          protoimpl.UnknownFields
	sizeCache              protoimpl.SizeCache
}

func (x *GuestCountersChanged) Reset() {
	*x = GuestCountersChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[33]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GuestCountersChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GuestCountersChanged) ProtoMessage() {}

func (x *GuestCountersChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[33]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GuestCountersChanged.ProtoReflect.Descriptor instead.
func (*GuestCountersChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{33}
}

func (x *GuestCountersChanged) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *GuestCountersChanged) GetPendingFriendRequests() uint32 {
	if x != nil {
		return x.PendingFriendRequests
	}
	return 0
}

func (x *GuestCountersChanged) GetLastStaffChatMessageId() int64 {
	if x != nil {
		return x.LastStaffChatMessageId
	}
	return 0
}

// Новое сообщение чата от сотрудника. Мгновенная. Заменяет опрос GET /api/guest/chat
// каждые 5 с (WindowsAgent/Frontend/main.js, setInterval 5000).
type ChatMessageReceived struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Кому (агент игнорирует, если вошёл другой гость).
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Сообщение.
	Message       *ChatMessage `protobuf:"bytes,2,opt,name=message,proto3" json:"message,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ChatMessageReceived) Reset() {
	*x = ChatMessageReceived{}
	mi := &file_agent_v1_agent_proto_msgTypes[34]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ChatMessageReceived) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ChatMessageReceived) ProtoMessage() {}

func (x *ChatMessageReceived) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[34]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ChatMessageReceived.ProtoReflect.Descriptor instead.
func (*ChatMessageReceived) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{34}
}

func (x *ChatMessageReceived) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *ChatMessageReceived) GetMessage() *ChatMessage {
	if x != nil {
		return x.Message
	}
	return nil
}

// Сообщение чата гостя с клубом.
type ChatMessage struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID (chat_messages.id).
	MessageId int64 `protobuf:"varint,1,opt,name=message_id,json=messageId,proto3" json:"message_id,omitempty"`
	// true — написал сотрудник клуба.
	FromStaff bool `protobuf:"varint,2,opt,name=from_staff,json=fromStaff,proto3" json:"from_staff,omitempty"`
	// Имя автора.
	AuthorName string `protobuf:"bytes,3,opt,name=author_name,json=authorName,proto3" json:"author_name,omitempty"`
	// Текст (≤ 1000 символов, ChatMessage::MAX_LENGTH).
	Body string `protobuf:"bytes,4,opt,name=body,proto3" json:"body,omitempty"`
	// Когда отправлено.
	SentAt *timestamppb.Timestamp `protobuf:"bytes,5,opt,name=sent_at,json=sentAt,proto3" json:"sent_at,omitempty"`
	// Когда прочитано получателем (пусто — не прочитано).
	ReadAt        *timestamppb.Timestamp `protobuf:"bytes,6,opt,name=read_at,json=readAt,proto3" json:"read_at,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ChatMessage) Reset() {
	*x = ChatMessage{}
	mi := &file_agent_v1_agent_proto_msgTypes[35]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ChatMessage) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ChatMessage) ProtoMessage() {}

func (x *ChatMessage) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[35]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ChatMessage.ProtoReflect.Descriptor instead.
func (*ChatMessage) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{35}
}

func (x *ChatMessage) GetMessageId() int64 {
	if x != nil {
		return x.MessageId
	}
	return 0
}

func (x *ChatMessage) GetFromStaff() bool {
	if x != nil {
		return x.FromStaff
	}
	return false
}

func (x *ChatMessage) GetAuthorName() string {
	if x != nil {
		return x.AuthorName
	}
	return ""
}

func (x *ChatMessage) GetBody() string {
	if x != nil {
		return x.Body
	}
	return ""
}

func (x *ChatMessage) GetSentAt() *timestamppb.Timestamp {
	if x != nil {
		return x.SentAt
	}
	return nil
}

func (x *ChatMessage) GetReadAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ReadAt
	}
	return nil
}

// Обновление агента. Долговременная (с expires_at). Старый аналог: agent_update
// {version, url, sha256} в heartbeat (AgentUpdate::offerFor), которое предлагалось, только
// когда ПК свободен и служба сообщила can_update.
//
// Проверка (агент, служба SYSTEM):
//  1. Подпись: Ed25519 (RFC 8032) по байтам UTF-8 строки
//     "zafiro-agent-update/v1\n" + version + "\n" + sha256 + "\n" + size_bytes + "\n" + channel
//     открытым ключом key_id из списка ключей, ВСТРОЕННЫХ в агент (ротация — новый ключ
//     приходит с обновлением, подписанным старым). Неизвестный key_id — отказ.
//  2. Скачать url (только HTTPS, хост из списка разрешённых), проверить size_bytes и sha256.
//  3. Любое несовпадение — удалить файл, CommandResult FAILED + UPDATE_VERIFICATION_FAILED.
//  4. Устанавливать только когда на ПК нет сессии (state IDLE), если install_now = false.
type UpdateAvailable struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Версия (SemVer).
	Version string `protobuf:"bytes,1,opt,name=version,proto3" json:"version,omitempty"`
	// HTTPS-адрес установщика.
	Url string `protobuf:"bytes,2,opt,name=url,proto3" json:"url,omitempty"`
	// SHA-256 установщика, 64 hex-символа в нижнем регистре.
	Sha256 string `protobuf:"bytes,3,opt,name=sha256,proto3" json:"sha256,omitempty"`
	// Размер установщика, байты.
	SizeBytes int64 `protobuf:"varint,4,opt,name=size_bytes,json=sizeBytes,proto3" json:"size_bytes,omitempty"`
	// Подпись Ed25519 (64 байта) по строке, описанной выше.
	SignatureEd25519 []byte `protobuf:"bytes,5,opt,name=signature_ed25519,json=signatureEd25519,proto3" json:"signature_ed25519,omitempty"`
	// Идентификатор ключа подписи (например "2026-10").
	KeyId string `protobuf:"bytes,6,opt,name=key_id,json=keyId,proto3" json:"key_id,omitempty"`
	// Канал выпуска ("stable", "beta").
	Channel string `protobuf:"bytes,7,opt,name=channel,proto3" json:"channel,omitempty"`
	// true — ставить сразу (критическое исправление), иначе — когда ПК свободен.
	InstallNow    bool `protobuf:"varint,8,opt,name=install_now,json=installNow,proto3" json:"install_now,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateAvailable) Reset() {
	*x = UpdateAvailable{}
	mi := &file_agent_v1_agent_proto_msgTypes[36]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateAvailable) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateAvailable) ProtoMessage() {}

func (x *UpdateAvailable) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[36]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateAvailable.ProtoReflect.Descriptor instead.
func (*UpdateAvailable) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{36}
}

func (x *UpdateAvailable) GetVersion() string {
	if x != nil {
		return x.Version
	}
	return ""
}

func (x *UpdateAvailable) GetUrl() string {
	if x != nil {
		return x.Url
	}
	return ""
}

func (x *UpdateAvailable) GetSha256() string {
	if x != nil {
		return x.Sha256
	}
	return ""
}

func (x *UpdateAvailable) GetSizeBytes() int64 {
	if x != nil {
		return x.SizeBytes
	}
	return 0
}

func (x *UpdateAvailable) GetSignatureEd25519() []byte {
	if x != nil {
		return x.SignatureEd25519
	}
	return nil
}

func (x *UpdateAvailable) GetKeyId() string {
	if x != nil {
		return x.KeyId
	}
	return ""
}

func (x *UpdateAvailable) GetChannel() string {
	if x != nil {
		return x.Channel
	}
	return ""
}

func (x *UpdateAvailable) GetInstallNow() bool {
	if x != nil {
		return x.InstallNow
	}
	return false
}

// Переводы изменились (правка суперадмина). Мгновенная.
type TranslationsChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Новая версия; агент вызывает GetTranslations, если она больше его.
	Version       int64 `protobuf:"varint,1,opt,name=version,proto3" json:"version,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *TranslationsChanged) Reset() {
	*x = TranslationsChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[37]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TranslationsChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TranslationsChanged) ProtoMessage() {}

func (x *TranslationsChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[37]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TranslationsChanged.ProtoReflect.Descriptor instead.
func (*TranslationsChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{37}
}

func (x *TranslationsChanged) GetVersion() int64 {
	if x != nil {
		return x.Version
	}
	return 0
}

// Настройки клуба изменились. Мгновенная (снимок всегда приходит в Welcome).
type ConfigChanged struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Новые настройки целиком.
	Config        *ClubConfig `protobuf:"bytes,1,opt,name=config,proto3" json:"config,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConfigChanged) Reset() {
	*x = ConfigChanged{}
	mi := &file_agent_v1_agent_proto_msgTypes[38]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConfigChanged) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConfigChanged) ProtoMessage() {}

func (x *ConfigChanged) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[38]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConfigChanged.ProtoReflect.Descriptor instead.
func (*ConfigChanged) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{38}
}

func (x *ConfigChanged) GetConfig() *ClubConfig {
	if x != nil {
		return x.Config
	}
	return nil
}

// Проверка связи. Мгновенная. Агент отвечает CommandResult(SUCCEEDED, agent_time).
type Ping struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Время сервера (синхронизация часов для отсчёта сессии).
	ServerTime    *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=server_time,json=serverTime,proto3" json:"server_time,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Ping) Reset() {
	*x = Ping{}
	mi := &file_agent_v1_agent_proto_msgTypes[39]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Ping) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Ping) ProtoMessage() {}

func (x *Ping) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[39]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Ping.ProtoReflect.Descriptor instead.
func (*Ping) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{39}
}

func (x *Ping) GetServerTime() *timestamppb.Timestamp {
	if x != nil {
		return x.ServerTime
	}
	return nil
}

// Разбудить выключенные ПК клуба по Wake-on-LAN. Долговременная (короткий expires_at).
// Старый аналог: wake_macs в heartbeat (≤ 50 адресов, StaffCommands.MaxWakeTargets).
type WakeOnLan struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// MAC-адреса "AA:BB:CC:DD:EE:FF", не больше 50.
	MacAddresses  []string `protobuf:"bytes,1,rep,name=mac_addresses,json=macAddresses,proto3" json:"mac_addresses,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *WakeOnLan) Reset() {
	*x = WakeOnLan{}
	mi := &file_agent_v1_agent_proto_msgTypes[40]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *WakeOnLan) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*WakeOnLan) ProtoMessage() {}

func (x *WakeOnLan) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[40]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use WakeOnLan.ProtoReflect.Descriptor instead.
func (*WakeOnLan) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{40}
}

func (x *WakeOnLan) GetMacAddresses() []string {
	if x != nil {
		return x.MacAddresses
	}
	return nil
}

// Выйти из аккаунта гостя (пароль сменён на другом устройстве, аккаунт заблокирован,
// сотрудник принудительно вывел). Долговременная.
type SignOutGuest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Кого выводить (агент игнорирует, если вошёл другой).
	UserId int64 `protobuf:"varint,1,opt,name=user_id,json=userId,proto3" json:"user_id,omitempty"`
	// Текст для гостя.
	Reason        string `protobuf:"bytes,2,opt,name=reason,proto3" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignOutGuest) Reset() {
	*x = SignOutGuest{}
	mi := &file_agent_v1_agent_proto_msgTypes[41]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignOutGuest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignOutGuest) ProtoMessage() {}

func (x *SignOutGuest) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[41]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignOutGuest.ProtoReflect.Descriptor instead.
func (*SignOutGuest) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{41}
}

func (x *SignOutGuest) GetUserId() int64 {
	if x != nil {
		return x.UserId
	}
	return 0
}

func (x *SignOutGuest) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

// QR-вход подтверждён. Мгновенная.
type QrLoginApproved struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// ID QR-входа (CreateQrLoginResponse.login_id).
	LoginId       string `protobuf:"bytes,1,opt,name=login_id,json=loginId,proto3" json:"login_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *QrLoginApproved) Reset() {
	*x = QrLoginApproved{}
	mi := &file_agent_v1_agent_proto_msgTypes[42]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *QrLoginApproved) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*QrLoginApproved) ProtoMessage() {}

func (x *QrLoginApproved) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[42]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use QrLoginApproved.ProtoReflect.Descriptor instead.
func (*QrLoginApproved) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{42}
}

func (x *QrLoginApproved) GetLoginId() string {
	if x != nil {
		return x.LoginId
	}
	return ""
}

// Сервер просит переподключиться (плановый перезапуск инстанса). Мгновенная.
type Drain struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Переподключиться через случайное время в [0, reconnect_within).
	ReconnectWithin *durationpb.Duration `protobuf:"bytes,1,opt,name=reconnect_within,json=reconnectWithin,proto3" json:"reconnect_within,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *Drain) Reset() {
	*x = Drain{}
	mi := &file_agent_v1_agent_proto_msgTypes[43]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Drain) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Drain) ProtoMessage() {}

func (x *Drain) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[43]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Drain.ProtoReflect.Descriptor instead.
func (*Drain) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{43}
}

func (x *Drain) GetReconnectWithin() *durationpb.Duration {
	if x != nil {
		return x.ReconnectWithin
	}
	return nil
}

// Запрос переводов.
type GetTranslationsRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Язык (ru, kk, en, ...).
	Locale string `protobuf:"bytes,1,opt,name=locale,proto3" json:"locale,omitempty"`
	// Версия, которая уже есть у агента: если не изменилась — not_modified = true без строк.
	KnownVersion  int64 `protobuf:"varint,2,opt,name=known_version,json=knownVersion,proto3" json:"known_version,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetTranslationsRequest) Reset() {
	*x = GetTranslationsRequest{}
	mi := &file_agent_v1_agent_proto_msgTypes[44]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetTranslationsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetTranslationsRequest) ProtoMessage() {}

func (x *GetTranslationsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[44]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetTranslationsRequest.ProtoReflect.Descriptor instead.
func (*GetTranslationsRequest) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{44}
}

func (x *GetTranslationsRequest) GetLocale() string {
	if x != nil {
		return x.Locale
	}
	return ""
}

func (x *GetTranslationsRequest) GetKnownVersion() int64 {
	if x != nil {
		return x.KnownVersion
	}
	return 0
}

// Ответ с переводами.
type GetTranslationsResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Язык.
	Locale string `protobuf:"bytes,1,opt,name=locale,proto3" json:"locale,omitempty"`
	// Версия переводов.
	Version int64 `protobuf:"varint,2,opt,name=version,proto3" json:"version,omitempty"`
	// true — known_version актуальна, lines пусто.
	NotModified bool `protobuf:"varint,3,opt,name=not_modified,json=notModified,proto3" json:"not_modified,omitempty"`
	// Строки группы `agent`: ключ → текст.
	Lines         map[string]string `protobuf:"bytes,4,rep,name=lines,proto3" json:"lines,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetTranslationsResponse) Reset() {
	*x = GetTranslationsResponse{}
	mi := &file_agent_v1_agent_proto_msgTypes[45]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetTranslationsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetTranslationsResponse) ProtoMessage() {}

func (x *GetTranslationsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[45]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetTranslationsResponse.ProtoReflect.Descriptor instead.
func (*GetTranslationsResponse) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{45}
}

func (x *GetTranslationsResponse) GetLocale() string {
	if x != nil {
		return x.Locale
	}
	return ""
}

func (x *GetTranslationsResponse) GetVersion() int64 {
	if x != nil {
		return x.Version
	}
	return 0
}

func (x *GetTranslationsResponse) GetNotModified() bool {
	if x != nil {
		return x.NotModified
	}
	return false
}

func (x *GetTranslationsResponse) GetLines() map[string]string {
	if x != nil {
		return x.Lines
	}
	return nil
}

// Запрос проверки PIN сотрудника.
type VerifyStaffPinRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// PIN (≤ 16 символов).
	Pin           string `protobuf:"bytes,1,opt,name=pin,proto3" json:"pin,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyStaffPinRequest) Reset() {
	*x = VerifyStaffPinRequest{}
	mi := &file_agent_v1_agent_proto_msgTypes[46]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyStaffPinRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyStaffPinRequest) ProtoMessage() {}

func (x *VerifyStaffPinRequest) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[46]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyStaffPinRequest.ProtoReflect.Descriptor instead.
func (*VerifyStaffPinRequest) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{46}
}

func (x *VerifyStaffPinRequest) GetPin() string {
	if x != nil {
		return x.Pin
	}
	return ""
}

// Ответ: PIN верный.
type VerifyStaffPinResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// На сколько минут служба закрывает агент (техрежим).
	StaffMinutes uint32 `protobuf:"varint,1,opt,name=staff_minutes,json=staffMinutes,proto3" json:"staff_minutes,omitempty"`
	// До какого времени.
	Until         *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=until,proto3" json:"until,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyStaffPinResponse) Reset() {
	*x = VerifyStaffPinResponse{}
	mi := &file_agent_v1_agent_proto_msgTypes[47]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyStaffPinResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyStaffPinResponse) ProtoMessage() {}

func (x *VerifyStaffPinResponse) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[47]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyStaffPinResponse.ProtoReflect.Descriptor instead.
func (*VerifyStaffPinResponse) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{47}
}

func (x *VerifyStaffPinResponse) GetStaffMinutes() uint32 {
	if x != nil {
		return x.StaffMinutes
	}
	return 0
}

func (x *VerifyStaffPinResponse) GetUntil() *timestamppb.Timestamp {
	if x != nil {
		return x.Until
	}
	return nil
}

// Запрос картинки.
type GetImageRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Вид.
	Kind ImageKind `protobuf:"varint,1,opt,name=kind,proto3,enum=zafiro.agent.v1.ImageKind" json:"kind,omitempty"`
	// ID новости (IMAGE_KIND_NEWS), иначе 0.
	Id int64 `protobuf:"varint,2,opt,name=id,proto3" json:"id,omitempty"`
	// ETag из прошлого ответа (кэш службы): совпал — not_modified.
	IfNoneMatch   string `protobuf:"bytes,3,opt,name=if_none_match,json=ifNoneMatch,proto3" json:"if_none_match,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetImageRequest) Reset() {
	*x = GetImageRequest{}
	mi := &file_agent_v1_agent_proto_msgTypes[48]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetImageRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetImageRequest) ProtoMessage() {}

func (x *GetImageRequest) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[48]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetImageRequest.ProtoReflect.Descriptor instead.
func (*GetImageRequest) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{48}
}

func (x *GetImageRequest) GetKind() ImageKind {
	if x != nil {
		return x.Kind
	}
	return ImageKind_IMAGE_KIND_UNSPECIFIED
}

func (x *GetImageRequest) GetId() int64 {
	if x != nil {
		return x.Id
	}
	return 0
}

func (x *GetImageRequest) GetIfNoneMatch() string {
	if x != nil {
		return x.IfNoneMatch
	}
	return ""
}

// Картинка.
type GetImageResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// true — etag совпал с if_none_match, data пусто.
	NotModified bool `protobuf:"varint,1,opt,name=not_modified,json=notModified,proto3" json:"not_modified,omitempty"`
	// Байты картинки (≤ 3 МиБ).
	Data []byte `protobuf:"bytes,2,opt,name=data,proto3" json:"data,omitempty"`
	// image/png или image/jpeg.
	ContentType string `protobuf:"bytes,3,opt,name=content_type,json=contentType,proto3" json:"content_type,omitempty"`
	// Отпечаток содержимого (SHA-256, hex).
	Etag          string `protobuf:"bytes,4,opt,name=etag,proto3" json:"etag,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetImageResponse) Reset() {
	*x = GetImageResponse{}
	mi := &file_agent_v1_agent_proto_msgTypes[49]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetImageResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetImageResponse) ProtoMessage() {}

func (x *GetImageResponse) ProtoReflect() protoreflect.Message {
	mi := &file_agent_v1_agent_proto_msgTypes[49]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetImageResponse.ProtoReflect.Descriptor instead.
func (*GetImageResponse) Descriptor() ([]byte, []int) {
	return file_agent_v1_agent_proto_rawDescGZIP(), []int{49}
}

func (x *GetImageResponse) GetNotModified() bool {
	if x != nil {
		return x.NotModified
	}
	return false
}

func (x *GetImageResponse) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

func (x *GetImageResponse) GetContentType() string {
	if x != nil {
		return x.ContentType
	}
	return ""
}

func (x *GetImageResponse) GetEtag() string {
	if x != nil {
		return x.Etag
	}
	return ""
}

var File_agent_v1_agent_proto protoreflect.FileDescriptor

const file_agent_v1_agent_proto_rawDesc = "" +
	"\n" +
	"\x14agent/v1/agent.proto\x12\x0fzafiro.agent.v1\x1a\x15agent/v1/common.proto\x1a\x19agent/v1/enrollment.proto\x1a\x1egoogle/protobuf/duration.proto\x1a\x1fgoogle/protobuf/timestamp.proto\"\xfa\x01\n" +
	"\fAgentMessage\x12.\n" +
	"\x05hello\x18\x01 \x01(\v2\x16.zafiro.agent.v1.HelloH\x00R\x05hello\x121\n" +
	"\x06status\x18\x02 \x01(\v2\x17.zafiro.agent.v1.StatusH\x00R\x06status\x123\n" +
	"\x05event\x18\x03 \x01(\v2\x1b.zafiro.agent.v1.AgentEventH\x00R\x05event\x12G\n" +
	"\x0ecommand_result\x18\x04 \x01(\v2\x1e.zafiro.agent.v1.CommandResultH\x00R\rcommandResultB\t\n" +
	"\apayload\"\x93\x03\n" +
	"\x05Hello\x12)\n" +
	"\x10protocol_version\x18\x01 \x01(\rR\x0fprotocolVersion\x12#\n" +
	"\ragent_version\x18\x02 \x01(\tR\fagentVersion\x12(\n" +
	"\x10last_command_seq\x18\x03 \x01(\x04R\x0elastCommandSeq\x12\x17\n" +
	"\aboot_id\x18\x04 \x01(\tR\x06bootId\x123\n" +
	"\x06device\x18\x05 \x01(\v2\x1b.zafiro.agent.v1.DeviceInfoR\x06device\x12F\n" +
	"\fcapabilities\x18\x06 \x01(\v2\".zafiro.agent.v1.AgentCapabilitiesR\fcapabilities\x12/\n" +
	"\x06status\x18\a \x01(\v2\x17.zafiro.agent.v1.StatusR\x06status\x121\n" +
	"\x14translations_version\x18\b \x01(\x03R\x13translationsVersion\x12\x16\n" +
	"\x06locale\x18\t \x01(\tR\x06locale\"\xbf\x01\n" +
	"\x11AgentCapabilities\x12\x1f\n" +
	"\vself_update\x18\x01 \x01(\bR\n" +
	"selfUpdate\x12\x1e\n" +
	"\vwake_on_lan\x18\x02 \x01(\bR\twakeOnLan\x12#\n" +
	"\rpower_control\x18\x03 \x01(\bR\fpowerControl\x12!\n" +
	"\fshow_message\x18\x04 \x01(\bR\vshowMessage\x12!\n" +
	"\fservice_mode\x18\x05 \x01(\bR\vserviceMode\"\xb1\x02\n" +
	"\x06Status\x124\n" +
	"\x05state\x18\x01 \x01(\x0e2\x1e.zafiro.agent.v1.ComputerStateR\x05state\x12\x1d\n" +
	"\n" +
	"session_id\x18\x02 \x01(\tR\tsessionId\x12)\n" +
	"\x11signed_in_user_id\x18\x03 \x01(\x03R\x0esignedInUserId\x121\n" +
	"\x04load\x18\x04 \x01(\v2\x1d.zafiro.agent.v1.ComputerLoadR\x04load\x12;\n" +
	"\vobserved_at\x18\x05 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"observedAt\x127\n" +
	"\x06health\x18\x06 \x01(\v2\x1f.zafiro.agent.v1.ComputerHealthR\x06health\"\xb4\x05\n" +
	"\x0eComputerHealth\x120\n" +
	"\x14cpu_temp_decicelsius\x18\x01 \x01(\x05R\x12cpuTempDecicelsius\x120\n" +
	"\x14gpu_temp_decicelsius\x18\x02 \x01(\x05R\x12gpuTempDecicelsius\x121\n" +
	"\x15gpu_load_basis_points\x18\x03 \x01(\rR\x12gpuLoadBasisPoints\x125\n" +
	"\x17gpu_memory_basis_points\x18\x04 \x01(\rR\x14gpuMemoryBasisPoints\x12!\n" +
	"\fgpu_measured\x18\x05 \x01(\bR\vgpuMeasured\x121\n" +
	"\x05disks\x18\x06 \x03(\v2\x1b.zafiro.agent.v1.DiskHealthR\x05disks\x12=\n" +
	"\vperipherals\x18\a \x03(\v2\x1b.zafiro.agent.v1.PeripheralR\vperipherals\x12&\n" +
	"\x0flink_speed_mbps\x18\b \x01(\rR\rlinkSpeedMbps\x12%\n" +
	"\x0euptime_seconds\x18\t \x01(\x04R\ruptimeSeconds\x126\n" +
	"\x17unexpected_shutdowns_7d\x18\n" +
	" \x01(\rR\x15unexpectedShutdowns7d\x12!\n" +
	"\fbugchecks_7d\x18\v \x01(\rR\vbugchecks7d\x12:\n" +
	"\x19unexpected_shutdowns_boot\x18\f \x01(\rR\x17unexpectedShutdownsBoot\x12%\n" +
	"\x0ebugchecks_boot\x18\r \x01(\rR\rbugchecksBoot\x122\n" +
	"\x15crashes_boot_measured\x18\x0e \x01(\bR\x13crashesBootMeasured\"\xa0\x01\n" +
	"\n" +
	"DiskHealth\x12\x14\n" +
	"\x05mount\x18\x01 \x01(\tR\x05mount\x12\x16\n" +
	"\x06system\x18\x02 \x01(\bR\x06system\x12\x19\n" +
	"\btotal_mb\x18\x03 \x01(\x04R\atotalMb\x12\x17\n" +
	"\afree_mb\x18\x04 \x01(\x04R\x06freeMb\x120\n" +
	"\x05smart\x18\x05 \x01(\x0e2\x1a.zafiro.agent.v1.DiskSmartR\x05smart\"s\n" +
	"\n" +
	"Peripheral\x123\n" +
	"\x04kind\x18\x01 \x01(\x0e2\x1f.zafiro.agent.v1.PeripheralKindR\x04kind\x12\x1c\n" +
	"\tconnected\x18\x02 \x01(\bR\tconnected\x12\x12\n" +
	"\x04name\x18\x03 \x01(\tR\x04name\"\xad\x01\n" +
	"\fComputerLoad\x12(\n" +
	"\x10cpu_basis_points\x18\x01 \x01(\rR\x0ecpuBasisPoints\x12(\n" +
	"\x10ram_basis_points\x18\x02 \x01(\rR\x0eramBasisPoints\x12!\n" +
	"\fidle_seconds\x18\x03 \x01(\rR\vidleSeconds\x12&\n" +
	"\x0fagent_memory_mb\x18\x04 \x01(\rR\ragentMemoryMb\"\x9d\x04\n" +
	"\n" +
	"AgentEvent\x12\x19\n" +
	"\bevent_id\x18\x01 \x01(\tR\aeventId\x12;\n" +
	"\voccurred_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"occurredAt\x12D\n" +
	"\rsession_ended\x18\x03 \x01(\v2\x1d.zafiro.agent.v1.SessionEndedH\x00R\fsessionEnded\x12H\n" +
	"\x0fguest_signed_in\x18\x04 \x01(\v2\x1e.zafiro.agent.v1.GuestSignedInH\x00R\rguestSignedIn\x12K\n" +
	"\x10guest_signed_out\x18\x05 \x01(\v2\x1f.zafiro.agent.v1.GuestSignedOutH\x00R\x0eguestSignedOut\x123\n" +
	"\x05error\x18\x06 \x01(\v2\x1b.zafiro.agent.v1.AgentErrorH\x00R\x05error\x12D\n" +
	"\rupdate_result\x18\a \x01(\v2\x1d.zafiro.agent.v1.UpdateResultH\x00R\fupdateResult\x12W\n" +
	"\x14service_mode_changed\x18\b \x01(\v2#.zafiro.agent.v1.ServiceModeChangedH\x00R\x12serviceModeChangedB\x06\n" +
	"\x04kind\"h\n" +
	"\fSessionEnded\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x129\n" +
	"\x06reason\x18\x02 \x01(\x0e2!.zafiro.agent.v1.SessionEndReasonR\x06reason\"_\n" +
	"\rGuestSignedIn\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x125\n" +
	"\x06method\x18\x02 \x01(\x0e2\x1d.zafiro.agent.v1.SignInMethodR\x06method\"a\n" +
	"\x0eGuestSignedOut\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x126\n" +
	"\x06reason\x18\x02 \x01(\x0e2\x1e.zafiro.agent.v1.SignOutReasonR\x06reason\"\x7f\n" +
	"\n" +
	"AgentError\x129\n" +
	"\n" +
	"error_code\x18\x01 \x01(\x0e2\x1a.zafiro.agent.v1.ErrorCodeR\terrorCode\x12\x1c\n" +
	"\tcomponent\x18\x02 \x01(\tR\tcomponent\x12\x18\n" +
	"\amessage\x18\x03 \x01(\tR\amessage\"}\n" +
	"\fUpdateResult\x12\x18\n" +
	"\aversion\x18\x01 \x01(\tR\aversion\x12\x18\n" +
	"\asuccess\x18\x02 \x01(\bR\asuccess\x129\n" +
	"\n" +
	"error_code\x18\x03 \x01(\x0e2\x1a.zafiro.agent.v1.ErrorCodeR\terrorCode\"^\n" +
	"\x12ServiceModeChanged\x12\x16\n" +
	"\x06active\x18\x01 \x01(\bR\x06active\x120\n" +
	"\x05until\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\x05until\"\xc7\x02\n" +
	"\rCommandResult\x12\x1d\n" +
	"\n" +
	"command_id\x18\x01 \x01(\tR\tcommandId\x12\x10\n" +
	"\x03seq\x18\x02 \x01(\x04R\x03seq\x126\n" +
	"\x06status\x18\x03 \x01(\x0e2\x1e.zafiro.agent.v1.CommandStatusR\x06status\x129\n" +
	"\n" +
	"error_code\x18\x04 \x01(\x0e2\x1a.zafiro.agent.v1.ErrorCodeR\terrorCode\x12\x18\n" +
	"\amessage\x18\x05 \x01(\tR\amessage\x12=\n" +
	"\fcompleted_at\x18\x06 \x01(\v2\x1a.google.protobuf.TimestampR\vcompletedAt\x129\n" +
	"\n" +
	"agent_time\x18\a \x01(\v2\x1a.google.protobuf.TimestampR\tagentTime\"\xc0\x01\n" +
	"\rServerMessage\x124\n" +
	"\awelcome\x18\x01 \x01(\v2\x18.zafiro.agent.v1.WelcomeH\x00R\awelcome\x124\n" +
	"\acommand\x18\x02 \x01(\v2\x18.zafiro.agent.v1.CommandH\x00R\acommand\x128\n" +
	"\tevent_ack\x18\x03 \x01(\v2\x19.zafiro.agent.v1.EventAckH\x00R\beventAckB\t\n" +
	"\apayload\"\x86\x05\n" +
	"\aWelcome\x12\x1f\n" +
	"\vcomputer_id\x18\x01 \x01(\x03R\n" +
	"computerId\x12\x17\n" +
	"\aclub_id\x18\x02 \x01(\x03R\x06clubId\x128\n" +
	"\bcomputer\x18\x03 \x01(\v2\x1c.zafiro.agent.v1.ComputerRefR\bcomputer\x12;\n" +
	"\vserver_time\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"serverTime\x12B\n" +
	"\x0fstatus_interval\x18\x05 \x01(\v2\x19.google.protobuf.DurationR\x0estatusInterval\x123\n" +
	"\x06config\x18\x06 \x01(\v2\x1b.zafiro.agent.v1.ClubConfigR\x06config\x128\n" +
	"\asession\x18\a \x01(\v2\x1e.zafiro.agent.v1.ActiveSessionR\asession\x121\n" +
	"\x14translations_version\x18\b \x01(\x03R\x13translationsVersion\x120\n" +
	"\x14min_protocol_version\x18\t \x01(\rR\x12minProtocolVersion\x120\n" +
	"\x14max_protocol_version\x18\n" +
	" \x01(\rR\x12maxProtocolVersion\x12,\n" +
	"\x12latest_command_seq\x18\v \x01(\x04R\x10latestCommandSeq\x12R\n" +
	"\x17certificate_renew_after\x18\f \x01(\v2\x1a.google.protobuf.TimestampR\x15certificateRenewAfter\"\xa8\x01\n" +
	"\rGuestFeatures\x12\x19\n" +
	"\bqr_login\x18\x01 \x01(\bR\aqrLogin\x12#\n" +
	"\rself_transfer\x18\x02 \x01(\bR\fselfTransfer\x12\x18\n" +
	"\afriends\x18\x03 \x01(\bR\afriends\x12)\n" +
	"\x10balance_transfer\x18\x04 \x01(\bR\x0fbalanceTransfer\x12\x12\n" +
	"\x04shop\x18\x05 \x01(\bR\x04shop\"2\n" +
	"\bLanguage\x12\x12\n" +
	"\x04code\x18\x01 \x01(\tR\x04code\x12\x12\n" +
	"\x04name\x18\x02 \x01(\tR\x04name\"\x88\x05\n" +
	"\n" +
	"ClubConfig\x12\x1b\n" +
	"\tclub_name\x18\x01 \x01(\tR\bclubName\x12\x19\n" +
	"\blogo_url\x18\x02 \x01(\tR\alogoUrl\x12(\n" +
	"\x10signin_image_url\x18\x03 \x01(\tR\x0esigninImageUrl\x12%\n" +
	"\x0edefault_locale\x18\x04 \x01(\tR\rdefaultLocale\x127\n" +
	"\tlanguages\x18\x05 \x03(\v2\x19.zafiro.agent.v1.LanguageR\tlanguages\x12U\n" +
	"\x14after_session_action\x18\x06 \x01(\x0e2#.zafiro.agent.v1.AfterSessionActionR\x12afterSessionAction\x12I\n" +
	"\x13after_session_delay\x18\a \x01(\v2\x19.google.protobuf.DurationR\x11afterSessionDelay\x12E\n" +
	"\x0eguest_features\x18\b \x01(\v2\x1e.zafiro.agent.v1.GuestFeaturesR\rguestFeatures\x12.\n" +
	"\x13idle_logout_minutes\x18\t \x01(\rR\x11idleLogoutMinutes\x12\x1a\n" +
	"\bcurrency\x18\n" +
	" \x01(\tR\bcurrency\x12#\n" +
	"\rstaff_minutes\x18\v \x01(\rR\fstaffMinutes\x12!\n" +
	"\fauto_billing\x18\f \x01(\bR\vautoBilling\x12;\n" +
	"\x1aauto_billing_pause_minutes\x18\r \x01(\rR\x17autoBillingPauseMinutes\"%\n" +
	"\bEventAck\x12\x19\n" +
	"\bevent_id\x18\x01 \x01(\tR\aeventId\"\xce\f\n" +
	"\aCommand\x12\x1d\n" +
	"\n" +
	"command_id\x18\x01 \x01(\tR\tcommandId\x12\x10\n" +
	"\x03seq\x18\x02 \x01(\x04R\x03seq\x127\n" +
	"\tissued_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\bissuedAt\x129\n" +
	"\n" +
	"expires_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\texpiresAt\x12)\n" +
	"\x11issued_by_user_id\x18\x05 \x01(\x03R\x0eissuedByUserId\x12D\n" +
	"\rstart_session\x18\n" +
	" \x01(\v2\x1d.zafiro.agent.v1.StartSessionH\x00R\fstartSession\x12>\n" +
	"\vend_session\x18\v \x01(\v2\x1b.zafiro.agent.v1.EndSessionH\x00R\n" +
	"endSession\x12+\n" +
	"\x04lock\x18\f \x01(\v2\x15.zafiro.agent.v1.LockH\x00R\x04lock\x121\n" +
	"\x06unlock\x18\r \x01(\v2\x17.zafiro.agent.v1.UnlockH\x00R\x06unlock\x121\n" +
	"\x06reboot\x18\x0e \x01(\v2\x17.zafiro.agent.v1.RebootH\x00R\x06reboot\x127\n" +
	"\bshutdown\x18\x0f \x01(\v2\x19.zafiro.agent.v1.ShutdownH\x00R\bshutdown\x12A\n" +
	"\fshow_message\x18\x10 \x01(\v2\x1c.zafiro.agent.v1.ShowMessageH\x00R\vshowMessage\x12J\n" +
	"\x0fbalance_changed\x18\x11 \x01(\v2\x1f.zafiro.agent.v1.BalanceChangedH\x00R\x0ebalanceChanged\x12M\n" +
	"\x10update_available\x18\x12 \x01(\v2 .zafiro.agent.v1.UpdateAvailableH\x00R\x0fupdateAvailable\x12Y\n" +
	"\x14translations_changed\x18\x13 \x01(\v2$.zafiro.agent.v1.TranslationsChangedH\x00R\x13translationsChanged\x12G\n" +
	"\x0econfig_changed\x18\x14 \x01(\v2\x1e.zafiro.agent.v1.ConfigChangedH\x00R\rconfigChanged\x12+\n" +
	"\x04ping\x18\x15 \x01(\v2\x15.zafiro.agent.v1.PingH\x00R\x04ping\x12Q\n" +
	"\x12enter_service_mode\x18\x16 \x01(\v2!.zafiro.agent.v1.EnterServiceModeH\x00R\x10enterServiceMode\x12<\n" +
	"\vwake_on_lan\x18\x17 \x01(\v2\x1a.zafiro.agent.v1.WakeOnLanH\x00R\twakeOnLan\x12E\n" +
	"\x0esign_out_guest\x18\x18 \x01(\v2\x1d.zafiro.agent.v1.SignOutGuestH\x00R\fsignOutGuest\x12]\n" +
	"\x16guest_counters_changed\x18\x19 \x01(\v2%.zafiro.agent.v1.GuestCountersChangedH\x00R\x14guestCountersChanged\x12Z\n" +
	"\x15chat_message_received\x18\x1a \x01(\v2$.zafiro.agent.v1.ChatMessageReceivedH\x00R\x13chatMessageReceived\x12N\n" +
	"\x11qr_login_approved\x18\x1b \x01(\v2 .zafiro.agent.v1.QrLoginApprovedH\x00R\x0fqrLoginApproved\x12.\n" +
	"\x05drain\x18\x1c \x01(\v2\x16.zafiro.agent.v1.DrainH\x00R\x05drain\x12W\n" +
	"\x14auto_billing_changed\x18\x1d \x01(\v2#.zafiro.agent.v1.AutoBillingChangedH\x00R\x12autoBillingChangedB\x06\n" +
	"\x04kind\"i\n" +
	"\x12AutoBillingChanged\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x12:\n" +
	"\x06status\x18\x02 \x01(\v2\".zafiro.agent.v1.AutoBillingStatusR\x06status\"H\n" +
	"\fStartSession\x128\n" +
	"\asession\x18\x01 \x01(\v2\x1e.zafiro.agent.v1.ActiveSessionR\asession\"\xe7\x01\n" +
	"\n" +
	"EndSession\x12\x1d\n" +
	"\n" +
	"session_id\x18\x01 \x01(\tR\tsessionId\x129\n" +
	"\x06reason\x18\x02 \x01(\x0e2!.zafiro.agent.v1.SessionEndReasonR\x06reason\x12,\n" +
	"\x12refunded_main_tiyn\x18\x03 \x01(\x03R\x10refundedMainTiyn\x12.\n" +
	"\x13refunded_bonus_tiyn\x18\x04 \x01(\x03R\x11refundedBonusTiyn\x12!\n" +
	"\fkept_minutes\x18\x05 \x01(\x05R\vkeptMinutes\"\x1e\n" +
	"\x04Lock\x12\x16\n" +
	"\x06reason\x18\x01 \x01(\tR\x06reason\"\b\n" +
	"\x06Unlock\"9\n" +
	"\x06Reboot\x12/\n" +
	"\x05delay\x18\x01 \x01(\v2\x19.google.protobuf.DurationR\x05delay\";\n" +
	"\bShutdown\x12/\n" +
	"\x05delay\x18\x01 \x01(\v2\x19.google.protobuf.DurationR\x05delay\"7\n" +
	"\x10EnterServiceMode\x12#\n" +
	"\rstaff_minutes\x18\x01 \x01(\rR\fstaffMinutes\"]\n" +
	"\vShowMessage\x12\x12\n" +
	"\x04text\x18\x01 \x01(\tR\x04text\x12:\n" +
	"\vdisplay_for\x18\x02 \x01(\v2\x19.google.protobuf.DurationR\n" +
	"displayFor\"]\n" +
	"\x0eBalanceChanged\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x122\n" +
	"\abalance\x18\x02 \x01(\v2\x18.zafiro.agent.v1.BalanceR\abalance\"\xa3\x01\n" +
	"\x14GuestCountersChanged\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x126\n" +
	"\x17pending_friend_requests\x18\x02 \x01(\rR\x15pendingFriendRequests\x12:\n" +
	"\x1alast_staff_chat_message_id\x18\x03 \x01(\x03R\x16lastStaffChatMessageId\"f\n" +
	"\x13ChatMessageReceived\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x126\n" +
	"\amessage\x18\x02 \x01(\v2\x1c.zafiro.agent.v1.ChatMessageR\amessage\"\xea\x01\n" +
	"\vChatMessage\x12\x1d\n" +
	"\n" +
	"message_id\x18\x01 \x01(\x03R\tmessageId\x12\x1d\n" +
	"\n" +
	"from_staff\x18\x02 \x01(\bR\tfromStaff\x12\x1f\n" +
	"\vauthor_name\x18\x03 \x01(\tR\n" +
	"authorName\x12\x12\n" +
	"\x04body\x18\x04 \x01(\tR\x04body\x123\n" +
	"\asent_at\x18\x05 \x01(\v2\x1a.google.protobuf.TimestampR\x06sentAt\x123\n" +
	"\aread_at\x18\x06 \x01(\v2\x1a.google.protobuf.TimestampR\x06readAt\"\xf3\x01\n" +
	"\x0fUpdateAvailable\x12\x18\n" +
	"\aversion\x18\x01 \x01(\tR\aversion\x12\x10\n" +
	"\x03url\x18\x02 \x01(\tR\x03url\x12\x16\n" +
	"\x06sha256\x18\x03 \x01(\tR\x06sha256\x12\x1d\n" +
	"\n" +
	"size_bytes\x18\x04 \x01(\x03R\tsizeBytes\x12+\n" +
	"\x11signature_ed25519\x18\x05 \x01(\fR\x10signatureEd25519\x12\x15\n" +
	"\x06key_id\x18\x06 \x01(\tR\x05keyId\x12\x18\n" +
	"\achannel\x18\a \x01(\tR\achannel\x12\x1f\n" +
	"\vinstall_now\x18\b \x01(\bR\n" +
	"installNow\"/\n" +
	"\x13TranslationsChanged\x12\x18\n" +
	"\aversion\x18\x01 \x01(\x03R\aversion\"D\n" +
	"\rConfigChanged\x123\n" +
	"\x06config\x18\x01 \x01(\v2\x1b.zafiro.agent.v1.ClubConfigR\x06config\"C\n" +
	"\x04Ping\x12;\n" +
	"\vserver_time\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"serverTime\"0\n" +
	"\tWakeOnLan\x12#\n" +
	"\rmac_addresses\x18\x01 \x03(\tR\fmacAddresses\"?\n" +
	"\fSignOutGuest\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\x03R\x06userId\x12\x16\n" +
	"\x06reason\x18\x02 \x01(\tR\x06reason\",\n" +
	"\x0fQrLoginApproved\x12\x19\n" +
	"\blogin_id\x18\x01 \x01(\tR\aloginId\"M\n" +
	"\x05Drain\x12D\n" +
	"\x10reconnect_within\x18\x01 \x01(\v2\x19.google.protobuf.DurationR\x0freconnectWithin\"U\n" +
	"\x16GetTranslationsRequest\x12\x16\n" +
	"\x06locale\x18\x01 \x01(\tR\x06locale\x12#\n" +
	"\rknown_version\x18\x02 \x01(\x03R\fknownVersion\"\xf3\x01\n" +
	"\x17GetTranslationsResponse\x12\x16\n" +
	"\x06locale\x18\x01 \x01(\tR\x06locale\x12\x18\n" +
	"\aversion\x18\x02 \x01(\x03R\aversion\x12!\n" +
	"\fnot_modified\x18\x03 \x01(\bR\vnotModified\x12I\n" +
	"\x05lines\x18\x04 \x03(\v23.zafiro.agent.v1.GetTranslationsResponse.LinesEntryR\x05lines\x1a8\n" +
	"\n" +
	"LinesEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01\")\n" +
	"\x15VerifyStaffPinRequest\x12\x10\n" +
	"\x03pin\x18\x01 \x01(\tR\x03pin\"o\n" +
	"\x16VerifyStaffPinResponse\x12#\n" +
	"\rstaff_minutes\x18\x01 \x01(\rR\fstaffMinutes\x120\n" +
	"\x05until\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\x05until\"u\n" +
	"\x0fGetImageRequest\x12.\n" +
	"\x04kind\x18\x01 \x01(\x0e2\x1a.zafiro.agent.v1.ImageKindR\x04kind\x12\x0e\n" +
	"\x02id\x18\x02 \x01(\x03R\x02id\x12\"\n" +
	"\rif_none_match\x18\x03 \x01(\tR\vifNoneMatch\"\x80\x01\n" +
	"\x10GetImageResponse\x12!\n" +
	"\fnot_modified\x18\x01 \x01(\bR\vnotModified\x12\x12\n" +
	"\x04data\x18\x02 \x01(\fR\x04data\x12!\n" +
	"\fcontent_type\x18\x03 \x01(\tR\vcontentType\x12\x12\n" +
	"\x04etag\x18\x04 \x01(\tR\x04etag*j\n" +
	"\tDiskSmart\x12\x1a\n" +
	"\x16DISK_SMART_UNSPECIFIED\x10\x00\x12\x11\n" +
	"\rDISK_SMART_OK\x10\x01\x12\x16\n" +
	"\x12DISK_SMART_WARNING\x10\x02\x12\x16\n" +
	"\x12DISK_SMART_FAILING\x10\x03*\xa4\x01\n" +
	"\x0ePeripheralKind\x12\x1f\n" +
	"\x1bPERIPHERAL_KIND_UNSPECIFIED\x10\x00\x12\x19\n" +
	"\x15PERIPHERAL_KIND_MOUSE\x10\x01\x12\x1c\n" +
	"\x18PERIPHERAL_KIND_KEYBOARD\x10\x02\x12\x1b\n" +
	"\x17PERIPHERAL_KIND_HEADSET\x10\x03\x12\x1b\n" +
	"\x17PERIPHERAL_KIND_MONITOR\x10\x04*\xd6\x01\n" +
	"\x10SessionEndReason\x12\"\n" +
	"\x1eSESSION_END_REASON_UNSPECIFIED\x10\x00\x12\x1e\n" +
	"\x1aSESSION_END_REASON_EXPIRED\x10\x01\x12\x1c\n" +
	"\x18SESSION_END_REASON_GUEST\x10\x02\x12\x1c\n" +
	"\x18SESSION_END_REASON_STAFF\x10\x03\x12$\n" +
	" SESSION_END_REASON_SEAT_TRANSFER\x10\x04\x12\x1c\n" +
	"\x18SESSION_END_REASON_POWER\x10\x05*\xa1\x01\n" +
	"\fSignInMethod\x12\x1e\n" +
	"\x1aSIGN_IN_METHOD_UNSPECIFIED\x10\x00\x12\x1b\n" +
	"\x17SIGN_IN_METHOD_PASSWORD\x10\x01\x12\x15\n" +
	"\x11SIGN_IN_METHOD_QR\x10\x02\x12\x1f\n" +
	"\x1bSIGN_IN_METHOD_REGISTRATION\x10\x03\x12\x1c\n" +
	"\x18SIGN_IN_METHOD_CASH_DESK\x10\x04*\xa3\x01\n" +
	"\rSignOutReason\x12\x1f\n" +
	"\x1bSIGN_OUT_REASON_UNSPECIFIED\x10\x00\x12\x18\n" +
	"\x14SIGN_OUT_REASON_USER\x10\x01\x12\x18\n" +
	"\x14SIGN_OUT_REASON_IDLE\x10\x02\x12\x1a\n" +
	"\x16SIGN_OUT_REASON_SERVER\x10\x03\x12!\n" +
	"\x1dSIGN_OUT_REASON_TOKEN_INVALID\x10\x04*\xa3\x01\n" +
	"\rCommandStatus\x12\x1e\n" +
	"\x1aCOMMAND_STATUS_UNSPECIFIED\x10\x00\x12\x1b\n" +
	"\x17COMMAND_STATUS_ACCEPTED\x10\x01\x12\x1c\n" +
	"\x18COMMAND_STATUS_SUCCEEDED\x10\x02\x12\x19\n" +
	"\x15COMMAND_STATUS_FAILED\x10\x03\x12\x1c\n" +
	"\x18COMMAND_STATUS_DUPLICATE\x10\x04*\x9e\x01\n" +
	"\x12AfterSessionAction\x12$\n" +
	" AFTER_SESSION_ACTION_UNSPECIFIED\x10\x00\x12\x1d\n" +
	"\x19AFTER_SESSION_ACTION_NONE\x10\x01\x12 \n" +
	"\x1cAFTER_SESSION_ACTION_RESTART\x10\x02\x12!\n" +
	"\x1dAFTER_SESSION_ACTION_SHUTDOWN\x10\x03*m\n" +
	"\tImageKind\x12\x1a\n" +
	"\x16IMAGE_KIND_UNSPECIFIED\x10\x00\x12\x18\n" +
	"\x14IMAGE_KIND_CLUB_LOGO\x10\x01\x12\x15\n" +
	"\x11IMAGE_KIND_SIGNIN\x10\x02\x12\x13\n" +
	"\x0fIMAGE_KIND_NEWS\x10\x032\xf6\x02\n" +
	"\fAgentService\x12L\n" +
	"\aConnect\x12\x1d.zafiro.agent.v1.AgentMessage\x1a\x1e.zafiro.agent.v1.ServerMessage(\x010\x01\x12d\n" +
	"\x0fGetTranslations\x12'.zafiro.agent.v1.GetTranslationsRequest\x1a(.zafiro.agent.v1.GetTranslationsResponse\x12a\n" +
	"\x0eVerifyStaffPin\x12&.zafiro.agent.v1.VerifyStaffPinRequest\x1a'.zafiro.agent.v1.VerifyStaffPinResponse\x12O\n" +
	"\bGetImage\x12 .zafiro.agent.v1.GetImageRequest\x1a!.zafiro.agent.v1.GetImageResponseBQZ=github.com/aitmaganbetov/zafirogo/server/gen/agent/v1;agentv1\xaa\x02\x0fZafiro.Agent.V1b\x06proto3"

var (
	file_agent_v1_agent_proto_rawDescOnce sync.Once
	file_agent_v1_agent_proto_rawDescData []byte
)

func file_agent_v1_agent_proto_rawDescGZIP() []byte {
	file_agent_v1_agent_proto_rawDescOnce.Do(func() {
		file_agent_v1_agent_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_agent_v1_agent_proto_rawDesc), len(file_agent_v1_agent_proto_rawDesc)))
	})
	return file_agent_v1_agent_proto_rawDescData
}

var file_agent_v1_agent_proto_enumTypes = make([]protoimpl.EnumInfo, 8)
var file_agent_v1_agent_proto_msgTypes = make([]protoimpl.MessageInfo, 51)
var file_agent_v1_agent_proto_goTypes = []any{
	(DiskSmart)(0),                  // 0: zafiro.agent.v1.DiskSmart
	(PeripheralKind)(0),             // 1: zafiro.agent.v1.PeripheralKind
	(SessionEndReason)(0),           // 2: zafiro.agent.v1.SessionEndReason
	(SignInMethod)(0),               // 3: zafiro.agent.v1.SignInMethod
	(SignOutReason)(0),              // 4: zafiro.agent.v1.SignOutReason
	(CommandStatus)(0),              // 5: zafiro.agent.v1.CommandStatus
	(AfterSessionAction)(0),         // 6: zafiro.agent.v1.AfterSessionAction
	(ImageKind)(0),                  // 7: zafiro.agent.v1.ImageKind
	(*AgentMessage)(nil),            // 8: zafiro.agent.v1.AgentMessage
	(*Hello)(nil),                   // 9: zafiro.agent.v1.Hello
	(*AgentCapabilities)(nil),       // 10: zafiro.agent.v1.AgentCapabilities
	(*Status)(nil),                  // 11: zafiro.agent.v1.Status
	(*ComputerHealth)(nil),          // 12: zafiro.agent.v1.ComputerHealth
	(*DiskHealth)(nil),              // 13: zafiro.agent.v1.DiskHealth
	(*Peripheral)(nil),              // 14: zafiro.agent.v1.Peripheral
	(*ComputerLoad)(nil),            // 15: zafiro.agent.v1.ComputerLoad
	(*AgentEvent)(nil),              // 16: zafiro.agent.v1.AgentEvent
	(*SessionEnded)(nil),            // 17: zafiro.agent.v1.SessionEnded
	(*GuestSignedIn)(nil),           // 18: zafiro.agent.v1.GuestSignedIn
	(*GuestSignedOut)(nil),          // 19: zafiro.agent.v1.GuestSignedOut
	(*AgentError)(nil),              // 20: zafiro.agent.v1.AgentError
	(*UpdateResult)(nil),            // 21: zafiro.agent.v1.UpdateResult
	(*ServiceModeChanged)(nil),      // 22: zafiro.agent.v1.ServiceModeChanged
	(*CommandResult)(nil),           // 23: zafiro.agent.v1.CommandResult
	(*ServerMessage)(nil),           // 24: zafiro.agent.v1.ServerMessage
	(*Welcome)(nil),                 // 25: zafiro.agent.v1.Welcome
	(*GuestFeatures)(nil),           // 26: zafiro.agent.v1.GuestFeatures
	(*Language)(nil),                // 27: zafiro.agent.v1.Language
	(*ClubConfig)(nil),              // 28: zafiro.agent.v1.ClubConfig
	(*EventAck)(nil),                // 29: zafiro.agent.v1.EventAck
	(*Command)(nil),                 // 30: zafiro.agent.v1.Command
	(*AutoBillingChanged)(nil),      // 31: zafiro.agent.v1.AutoBillingChanged
	(*StartSession)(nil),            // 32: zafiro.agent.v1.StartSession
	(*EndSession)(nil),              // 33: zafiro.agent.v1.EndSession
	(*Lock)(nil),                    // 34: zafiro.agent.v1.Lock
	(*Unlock)(nil),                  // 35: zafiro.agent.v1.Unlock
	(*Reboot)(nil),                  // 36: zafiro.agent.v1.Reboot
	(*Shutdown)(nil),                // 37: zafiro.agent.v1.Shutdown
	(*EnterServiceMode)(nil),        // 38: zafiro.agent.v1.EnterServiceMode
	(*ShowMessage)(nil),             // 39: zafiro.agent.v1.ShowMessage
	(*BalanceChanged)(nil),          // 40: zafiro.agent.v1.BalanceChanged
	(*GuestCountersChanged)(nil),    // 41: zafiro.agent.v1.GuestCountersChanged
	(*ChatMessageReceived)(nil),     // 42: zafiro.agent.v1.ChatMessageReceived
	(*ChatMessage)(nil),             // 43: zafiro.agent.v1.ChatMessage
	(*UpdateAvailable)(nil),         // 44: zafiro.agent.v1.UpdateAvailable
	(*TranslationsChanged)(nil),     // 45: zafiro.agent.v1.TranslationsChanged
	(*ConfigChanged)(nil),           // 46: zafiro.agent.v1.ConfigChanged
	(*Ping)(nil),                    // 47: zafiro.agent.v1.Ping
	(*WakeOnLan)(nil),               // 48: zafiro.agent.v1.WakeOnLan
	(*SignOutGuest)(nil),            // 49: zafiro.agent.v1.SignOutGuest
	(*QrLoginApproved)(nil),         // 50: zafiro.agent.v1.QrLoginApproved
	(*Drain)(nil),                   // 51: zafiro.agent.v1.Drain
	(*GetTranslationsRequest)(nil),  // 52: zafiro.agent.v1.GetTranslationsRequest
	(*GetTranslationsResponse)(nil), // 53: zafiro.agent.v1.GetTranslationsResponse
	(*VerifyStaffPinRequest)(nil),   // 54: zafiro.agent.v1.VerifyStaffPinRequest
	(*VerifyStaffPinResponse)(nil),  // 55: zafiro.agent.v1.VerifyStaffPinResponse
	(*GetImageRequest)(nil),         // 56: zafiro.agent.v1.GetImageRequest
	(*GetImageResponse)(nil),        // 57: zafiro.agent.v1.GetImageResponse
	nil,                             // 58: zafiro.agent.v1.GetTranslationsResponse.LinesEntry
	(*DeviceInfo)(nil),              // 59: zafiro.agent.v1.DeviceInfo
	(ComputerState)(0),              // 60: zafiro.agent.v1.ComputerState
	(*timestamppb.Timestamp)(nil),   // 61: google.protobuf.Timestamp
	(ErrorCode)(0),                  // 62: zafiro.agent.v1.ErrorCode
	(*ComputerRef)(nil),             // 63: zafiro.agent.v1.ComputerRef
	(*durationpb.Duration)(nil),     // 64: google.protobuf.Duration
	(*ActiveSession)(nil),           // 65: zafiro.agent.v1.ActiveSession
	(*AutoBillingStatus)(nil),       // 66: zafiro.agent.v1.AutoBillingStatus
	(*Balance)(nil),                 // 67: zafiro.agent.v1.Balance
}
var file_agent_v1_agent_proto_depIdxs = []int32{
	9,  // 0: zafiro.agent.v1.AgentMessage.hello:type_name -> zafiro.agent.v1.Hello
	11, // 1: zafiro.agent.v1.AgentMessage.status:type_name -> zafiro.agent.v1.Status
	16, // 2: zafiro.agent.v1.AgentMessage.event:type_name -> zafiro.agent.v1.AgentEvent
	23, // 3: zafiro.agent.v1.AgentMessage.command_result:type_name -> zafiro.agent.v1.CommandResult
	59, // 4: zafiro.agent.v1.Hello.device:type_name -> zafiro.agent.v1.DeviceInfo
	10, // 5: zafiro.agent.v1.Hello.capabilities:type_name -> zafiro.agent.v1.AgentCapabilities
	11, // 6: zafiro.agent.v1.Hello.status:type_name -> zafiro.agent.v1.Status
	60, // 7: zafiro.agent.v1.Status.state:type_name -> zafiro.agent.v1.ComputerState
	15, // 8: zafiro.agent.v1.Status.load:type_name -> zafiro.agent.v1.ComputerLoad
	61, // 9: zafiro.agent.v1.Status.observed_at:type_name -> google.protobuf.Timestamp
	12, // 10: zafiro.agent.v1.Status.health:type_name -> zafiro.agent.v1.ComputerHealth
	13, // 11: zafiro.agent.v1.ComputerHealth.disks:type_name -> zafiro.agent.v1.DiskHealth
	14, // 12: zafiro.agent.v1.ComputerHealth.peripherals:type_name -> zafiro.agent.v1.Peripheral
	0,  // 13: zafiro.agent.v1.DiskHealth.smart:type_name -> zafiro.agent.v1.DiskSmart
	1,  // 14: zafiro.agent.v1.Peripheral.kind:type_name -> zafiro.agent.v1.PeripheralKind
	61, // 15: zafiro.agent.v1.AgentEvent.occurred_at:type_name -> google.protobuf.Timestamp
	17, // 16: zafiro.agent.v1.AgentEvent.session_ended:type_name -> zafiro.agent.v1.SessionEnded
	18, // 17: zafiro.agent.v1.AgentEvent.guest_signed_in:type_name -> zafiro.agent.v1.GuestSignedIn
	19, // 18: zafiro.agent.v1.AgentEvent.guest_signed_out:type_name -> zafiro.agent.v1.GuestSignedOut
	20, // 19: zafiro.agent.v1.AgentEvent.error:type_name -> zafiro.agent.v1.AgentError
	21, // 20: zafiro.agent.v1.AgentEvent.update_result:type_name -> zafiro.agent.v1.UpdateResult
	22, // 21: zafiro.agent.v1.AgentEvent.service_mode_changed:type_name -> zafiro.agent.v1.ServiceModeChanged
	2,  // 22: zafiro.agent.v1.SessionEnded.reason:type_name -> zafiro.agent.v1.SessionEndReason
	3,  // 23: zafiro.agent.v1.GuestSignedIn.method:type_name -> zafiro.agent.v1.SignInMethod
	4,  // 24: zafiro.agent.v1.GuestSignedOut.reason:type_name -> zafiro.agent.v1.SignOutReason
	62, // 25: zafiro.agent.v1.AgentError.error_code:type_name -> zafiro.agent.v1.ErrorCode
	62, // 26: zafiro.agent.v1.UpdateResult.error_code:type_name -> zafiro.agent.v1.ErrorCode
	61, // 27: zafiro.agent.v1.ServiceModeChanged.until:type_name -> google.protobuf.Timestamp
	5,  // 28: zafiro.agent.v1.CommandResult.status:type_name -> zafiro.agent.v1.CommandStatus
	62, // 29: zafiro.agent.v1.CommandResult.error_code:type_name -> zafiro.agent.v1.ErrorCode
	61, // 30: zafiro.agent.v1.CommandResult.completed_at:type_name -> google.protobuf.Timestamp
	61, // 31: zafiro.agent.v1.CommandResult.agent_time:type_name -> google.protobuf.Timestamp
	25, // 32: zafiro.agent.v1.ServerMessage.welcome:type_name -> zafiro.agent.v1.Welcome
	30, // 33: zafiro.agent.v1.ServerMessage.command:type_name -> zafiro.agent.v1.Command
	29, // 34: zafiro.agent.v1.ServerMessage.event_ack:type_name -> zafiro.agent.v1.EventAck
	63, // 35: zafiro.agent.v1.Welcome.computer:type_name -> zafiro.agent.v1.ComputerRef
	61, // 36: zafiro.agent.v1.Welcome.server_time:type_name -> google.protobuf.Timestamp
	64, // 37: zafiro.agent.v1.Welcome.status_interval:type_name -> google.protobuf.Duration
	28, // 38: zafiro.agent.v1.Welcome.config:type_name -> zafiro.agent.v1.ClubConfig
	65, // 39: zafiro.agent.v1.Welcome.session:type_name -> zafiro.agent.v1.ActiveSession
	61, // 40: zafiro.agent.v1.Welcome.certificate_renew_after:type_name -> google.protobuf.Timestamp
	27, // 41: zafiro.agent.v1.ClubConfig.languages:type_name -> zafiro.agent.v1.Language
	6,  // 42: zafiro.agent.v1.ClubConfig.after_session_action:type_name -> zafiro.agent.v1.AfterSessionAction
	64, // 43: zafiro.agent.v1.ClubConfig.after_session_delay:type_name -> google.protobuf.Duration
	26, // 44: zafiro.agent.v1.ClubConfig.guest_features:type_name -> zafiro.agent.v1.GuestFeatures
	61, // 45: zafiro.agent.v1.Command.issued_at:type_name -> google.protobuf.Timestamp
	61, // 46: zafiro.agent.v1.Command.expires_at:type_name -> google.protobuf.Timestamp
	32, // 47: zafiro.agent.v1.Command.start_session:type_name -> zafiro.agent.v1.StartSession
	33, // 48: zafiro.agent.v1.Command.end_session:type_name -> zafiro.agent.v1.EndSession
	34, // 49: zafiro.agent.v1.Command.lock:type_name -> zafiro.agent.v1.Lock
	35, // 50: zafiro.agent.v1.Command.unlock:type_name -> zafiro.agent.v1.Unlock
	36, // 51: zafiro.agent.v1.Command.reboot:type_name -> zafiro.agent.v1.Reboot
	37, // 52: zafiro.agent.v1.Command.shutdown:type_name -> zafiro.agent.v1.Shutdown
	39, // 53: zafiro.agent.v1.Command.show_message:type_name -> zafiro.agent.v1.ShowMessage
	40, // 54: zafiro.agent.v1.Command.balance_changed:type_name -> zafiro.agent.v1.BalanceChanged
	44, // 55: zafiro.agent.v1.Command.update_available:type_name -> zafiro.agent.v1.UpdateAvailable
	45, // 56: zafiro.agent.v1.Command.translations_changed:type_name -> zafiro.agent.v1.TranslationsChanged
	46, // 57: zafiro.agent.v1.Command.config_changed:type_name -> zafiro.agent.v1.ConfigChanged
	47, // 58: zafiro.agent.v1.Command.ping:type_name -> zafiro.agent.v1.Ping
	38, // 59: zafiro.agent.v1.Command.enter_service_mode:type_name -> zafiro.agent.v1.EnterServiceMode
	48, // 60: zafiro.agent.v1.Command.wake_on_lan:type_name -> zafiro.agent.v1.WakeOnLan
	49, // 61: zafiro.agent.v1.Command.sign_out_guest:type_name -> zafiro.agent.v1.SignOutGuest
	41, // 62: zafiro.agent.v1.Command.guest_counters_changed:type_name -> zafiro.agent.v1.GuestCountersChanged
	42, // 63: zafiro.agent.v1.Command.chat_message_received:type_name -> zafiro.agent.v1.ChatMessageReceived
	50, // 64: zafiro.agent.v1.Command.qr_login_approved:type_name -> zafiro.agent.v1.QrLoginApproved
	51, // 65: zafiro.agent.v1.Command.drain:type_name -> zafiro.agent.v1.Drain
	31, // 66: zafiro.agent.v1.Command.auto_billing_changed:type_name -> zafiro.agent.v1.AutoBillingChanged
	66, // 67: zafiro.agent.v1.AutoBillingChanged.status:type_name -> zafiro.agent.v1.AutoBillingStatus
	65, // 68: zafiro.agent.v1.StartSession.session:type_name -> zafiro.agent.v1.ActiveSession
	2,  // 69: zafiro.agent.v1.EndSession.reason:type_name -> zafiro.agent.v1.SessionEndReason
	64, // 70: zafiro.agent.v1.Reboot.delay:type_name -> google.protobuf.Duration
	64, // 71: zafiro.agent.v1.Shutdown.delay:type_name -> google.protobuf.Duration
	64, // 72: zafiro.agent.v1.ShowMessage.display_for:type_name -> google.protobuf.Duration
	67, // 73: zafiro.agent.v1.BalanceChanged.balance:type_name -> zafiro.agent.v1.Balance
	43, // 74: zafiro.agent.v1.ChatMessageReceived.message:type_name -> zafiro.agent.v1.ChatMessage
	61, // 75: zafiro.agent.v1.ChatMessage.sent_at:type_name -> google.protobuf.Timestamp
	61, // 76: zafiro.agent.v1.ChatMessage.read_at:type_name -> google.protobuf.Timestamp
	28, // 77: zafiro.agent.v1.ConfigChanged.config:type_name -> zafiro.agent.v1.ClubConfig
	61, // 78: zafiro.agent.v1.Ping.server_time:type_name -> google.protobuf.Timestamp
	64, // 79: zafiro.agent.v1.Drain.reconnect_within:type_name -> google.protobuf.Duration
	58, // 80: zafiro.agent.v1.GetTranslationsResponse.lines:type_name -> zafiro.agent.v1.GetTranslationsResponse.LinesEntry
	61, // 81: zafiro.agent.v1.VerifyStaffPinResponse.until:type_name -> google.protobuf.Timestamp
	7,  // 82: zafiro.agent.v1.GetImageRequest.kind:type_name -> zafiro.agent.v1.ImageKind
	8,  // 83: zafiro.agent.v1.AgentService.Connect:input_type -> zafiro.agent.v1.AgentMessage
	52, // 84: zafiro.agent.v1.AgentService.GetTranslations:input_type -> zafiro.agent.v1.GetTranslationsRequest
	54, // 85: zafiro.agent.v1.AgentService.VerifyStaffPin:input_type -> zafiro.agent.v1.VerifyStaffPinRequest
	56, // 86: zafiro.agent.v1.AgentService.GetImage:input_type -> zafiro.agent.v1.GetImageRequest
	24, // 87: zafiro.agent.v1.AgentService.Connect:output_type -> zafiro.agent.v1.ServerMessage
	53, // 88: zafiro.agent.v1.AgentService.GetTranslations:output_type -> zafiro.agent.v1.GetTranslationsResponse
	55, // 89: zafiro.agent.v1.AgentService.VerifyStaffPin:output_type -> zafiro.agent.v1.VerifyStaffPinResponse
	57, // 90: zafiro.agent.v1.AgentService.GetImage:output_type -> zafiro.agent.v1.GetImageResponse
	87, // [87:91] is the sub-list for method output_type
	83, // [83:87] is the sub-list for method input_type
	83, // [83:83] is the sub-list for extension type_name
	83, // [83:83] is the sub-list for extension extendee
	0,  // [0:83] is the sub-list for field type_name
}

func init() { file_agent_v1_agent_proto_init() }
func file_agent_v1_agent_proto_init() {
	if File_agent_v1_agent_proto != nil {
		return
	}
	file_agent_v1_common_proto_init()
	file_agent_v1_enrollment_proto_init()
	file_agent_v1_agent_proto_msgTypes[0].OneofWrappers = []any{
		(*AgentMessage_Hello)(nil),
		(*AgentMessage_Status)(nil),
		(*AgentMessage_Event)(nil),
		(*AgentMessage_CommandResult)(nil),
	}
	file_agent_v1_agent_proto_msgTypes[8].OneofWrappers = []any{
		(*AgentEvent_SessionEnded)(nil),
		(*AgentEvent_GuestSignedIn)(nil),
		(*AgentEvent_GuestSignedOut)(nil),
		(*AgentEvent_Error)(nil),
		(*AgentEvent_UpdateResult)(nil),
		(*AgentEvent_ServiceModeChanged)(nil),
	}
	file_agent_v1_agent_proto_msgTypes[16].OneofWrappers = []any{
		(*ServerMessage_Welcome)(nil),
		(*ServerMessage_Command)(nil),
		(*ServerMessage_EventAck)(nil),
	}
	file_agent_v1_agent_proto_msgTypes[22].OneofWrappers = []any{
		(*Command_StartSession)(nil),
		(*Command_EndSession)(nil),
		(*Command_Lock)(nil),
		(*Command_Unlock)(nil),
		(*Command_Reboot)(nil),
		(*Command_Shutdown)(nil),
		(*Command_ShowMessage)(nil),
		(*Command_BalanceChanged)(nil),
		(*Command_UpdateAvailable)(nil),
		(*Command_TranslationsChanged)(nil),
		(*Command_ConfigChanged)(nil),
		(*Command_Ping)(nil),
		(*Command_EnterServiceMode)(nil),
		(*Command_WakeOnLan)(nil),
		(*Command_SignOutGuest)(nil),
		(*Command_GuestCountersChanged)(nil),
		(*Command_ChatMessageReceived)(nil),
		(*Command_QrLoginApproved)(nil),
		(*Command_Drain)(nil),
		(*Command_AutoBillingChanged)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_agent_v1_agent_proto_rawDesc), len(file_agent_v1_agent_proto_rawDesc)),
			NumEnums:      8,
			NumMessages:   51,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_agent_v1_agent_proto_goTypes,
		DependencyIndexes: file_agent_v1_agent_proto_depIdxs,
		EnumInfos:         file_agent_v1_agent_proto_enumTypes,
		MessageInfos:      file_agent_v1_agent_proto_msgTypes,
	}.Build()
	File_agent_v1_agent_proto = out.File
	file_agent_v1_agent_proto_goTypes = nil
	file_agent_v1_agent_proto_depIdxs = nil
}
