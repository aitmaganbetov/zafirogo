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

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.6.2
// - protoc             (unknown)
// source: agent/v1/agent.proto

package agentv1

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	AgentService_Connect_FullMethodName         = "/zafiro.agent.v1.AgentService/Connect"
	AgentService_GetTranslations_FullMethodName = "/zafiro.agent.v1.AgentService/GetTranslations"
	AgentService_VerifyStaffPin_FullMethodName  = "/zafiro.agent.v1.AgentService/VerifyStaffPin"
)

// AgentServiceClient is the client API for AgentService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// Основной канал ПК ↔ сервер (под mTLS).
type AgentServiceClient interface {
	// Двунаправленный стрим на всё время работы службы агента.
	// Агент первым сообщением шлёт Hello, сервер отвечает Welcome; дальше — Status/события
	// от агента и команды от сервера. Порядок и повторная доставка — см. шапку файла (п. 6).
	//
	// Заменяет: POST /api/agent/heartbeat, POST /api/club-computers/{id}/heartbeat, ответы
	// heartbeat с командами (power_command, staff_message, wake_macs, agent_update, customer,
	// session, club), опрос QR-входа, опрос счётчиков чата/друзей и баланса.
	//
	// buf:lint:ignore RPC_REQUEST_STANDARD_NAME
	// buf:lint:ignore RPC_RESPONSE_STANDARD_NAME
	Connect(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[AgentMessage, ServerMessage], error)
	// Тексты интерфейса агента на одном языке с правками суперадмина.
	// Заменяет GET /api/agent/translations/{locale} (AgentTranslationController,
	// throttle agent-translations: 600/мин на IP). Теперь под mTLS, лимит 30/мин на ПК.
	// Агент запрашивает, когда версия в Welcome/TranslationsChanged новее своей.
	GetTranslations(ctx context.Context, in *GetTranslationsRequest, opts ...grpc.CallOption) (*GetTranslationsResponse, error)
	// Проверка PIN сотрудника для выхода из агента (Ctrl+Alt+P) / техрежима.
	// Заменяет POST /api/agent/exit-pin (AgentExitPinController). Лимиты как
	// RateLimiter `agent-exit-pin`: 5 попыток за 5 минут на ПК и 100 в час на клуб.
	// Если в метаданных есть токен гостя — сервер пишет в журнал, при ком закрыли агент.
	// Ошибки: PIN_NOT_CONFIGURED, INVALID_PIN, RATE_LIMITED.
	VerifyStaffPin(ctx context.Context, in *VerifyStaffPinRequest, opts ...grpc.CallOption) (*VerifyStaffPinResponse, error)
}

type agentServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewAgentServiceClient(cc grpc.ClientConnInterface) AgentServiceClient {
	return &agentServiceClient{cc}
}

func (c *agentServiceClient) Connect(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[AgentMessage, ServerMessage], error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	stream, err := c.cc.NewStream(ctx, &AgentService_ServiceDesc.Streams[0], AgentService_Connect_FullMethodName, cOpts...)
	if err != nil {
		return nil, err
	}
	x := &grpc.GenericClientStream[AgentMessage, ServerMessage]{ClientStream: stream}
	return x, nil
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type AgentService_ConnectClient = grpc.BidiStreamingClient[AgentMessage, ServerMessage]

func (c *agentServiceClient) GetTranslations(ctx context.Context, in *GetTranslationsRequest, opts ...grpc.CallOption) (*GetTranslationsResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetTranslationsResponse)
	err := c.cc.Invoke(ctx, AgentService_GetTranslations_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentServiceClient) VerifyStaffPin(ctx context.Context, in *VerifyStaffPinRequest, opts ...grpc.CallOption) (*VerifyStaffPinResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(VerifyStaffPinResponse)
	err := c.cc.Invoke(ctx, AgentService_VerifyStaffPin_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AgentServiceServer is the server API for AgentService service.
// All implementations must embed UnimplementedAgentServiceServer
// for forward compatibility.
//
// Основной канал ПК ↔ сервер (под mTLS).
type AgentServiceServer interface {
	// Двунаправленный стрим на всё время работы службы агента.
	// Агент первым сообщением шлёт Hello, сервер отвечает Welcome; дальше — Status/события
	// от агента и команды от сервера. Порядок и повторная доставка — см. шапку файла (п. 6).
	//
	// Заменяет: POST /api/agent/heartbeat, POST /api/club-computers/{id}/heartbeat, ответы
	// heartbeat с командами (power_command, staff_message, wake_macs, agent_update, customer,
	// session, club), опрос QR-входа, опрос счётчиков чата/друзей и баланса.
	//
	// buf:lint:ignore RPC_REQUEST_STANDARD_NAME
	// buf:lint:ignore RPC_RESPONSE_STANDARD_NAME
	Connect(grpc.BidiStreamingServer[AgentMessage, ServerMessage]) error
	// Тексты интерфейса агента на одном языке с правками суперадмина.
	// Заменяет GET /api/agent/translations/{locale} (AgentTranslationController,
	// throttle agent-translations: 600/мин на IP). Теперь под mTLS, лимит 30/мин на ПК.
	// Агент запрашивает, когда версия в Welcome/TranslationsChanged новее своей.
	GetTranslations(context.Context, *GetTranslationsRequest) (*GetTranslationsResponse, error)
	// Проверка PIN сотрудника для выхода из агента (Ctrl+Alt+P) / техрежима.
	// Заменяет POST /api/agent/exit-pin (AgentExitPinController). Лимиты как
	// RateLimiter `agent-exit-pin`: 5 попыток за 5 минут на ПК и 100 в час на клуб.
	// Если в метаданных есть токен гостя — сервер пишет в журнал, при ком закрыли агент.
	// Ошибки: PIN_NOT_CONFIGURED, INVALID_PIN, RATE_LIMITED.
	VerifyStaffPin(context.Context, *VerifyStaffPinRequest) (*VerifyStaffPinResponse, error)
	mustEmbedUnimplementedAgentServiceServer()
}

// UnimplementedAgentServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedAgentServiceServer struct{}

func (UnimplementedAgentServiceServer) Connect(grpc.BidiStreamingServer[AgentMessage, ServerMessage]) error {
	return status.Error(codes.Unimplemented, "method Connect not implemented")
}
func (UnimplementedAgentServiceServer) GetTranslations(context.Context, *GetTranslationsRequest) (*GetTranslationsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method GetTranslations not implemented")
}
func (UnimplementedAgentServiceServer) VerifyStaffPin(context.Context, *VerifyStaffPinRequest) (*VerifyStaffPinResponse, error) {
	return nil, status.Error(codes.Unimplemented, "method VerifyStaffPin not implemented")
}
func (UnimplementedAgentServiceServer) mustEmbedUnimplementedAgentServiceServer() {}
func (UnimplementedAgentServiceServer) testEmbeddedByValue()                      {}

// UnsafeAgentServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to AgentServiceServer will
// result in compilation errors.
type UnsafeAgentServiceServer interface {
	mustEmbedUnimplementedAgentServiceServer()
}

func RegisterAgentServiceServer(s grpc.ServiceRegistrar, srv AgentServiceServer) {
	// If the following call panics, it indicates UnimplementedAgentServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&AgentService_ServiceDesc, srv)
}

func _AgentService_Connect_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(AgentServiceServer).Connect(&grpc.GenericServerStream[AgentMessage, ServerMessage]{ServerStream: stream})
}

// This type alias is provided for backwards compatibility with existing code that references the prior non-generic stream type by name.
type AgentService_ConnectServer = grpc.BidiStreamingServer[AgentMessage, ServerMessage]

func _AgentService_GetTranslations_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetTranslationsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).GetTranslations(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: AgentService_GetTranslations_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AgentServiceServer).GetTranslations(ctx, req.(*GetTranslationsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AgentService_VerifyStaffPin_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(VerifyStaffPinRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).VerifyStaffPin(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: AgentService_VerifyStaffPin_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AgentServiceServer).VerifyStaffPin(ctx, req.(*VerifyStaffPinRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// AgentService_ServiceDesc is the grpc.ServiceDesc for AgentService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var AgentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "zafiro.agent.v1.AgentService",
	HandlerType: (*AgentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetTranslations",
			Handler:    _AgentService_GetTranslations_Handler,
		},
		{
			MethodName: "VerifyStaffPin",
			Handler:    _AgentService_VerifyStaffPin_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Connect",
			Handler:       _AgentService_Connect_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "agent/v1/agent.proto",
}
