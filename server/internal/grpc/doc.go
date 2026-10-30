// Package grpcserver — gRPC-сервер для агентов ПК: TLS с необязательным клиентским сертификатом
// (Enroll — без сертификата, всё остальное — mTLS), перехватчики проверки сертификата, ошибки
// google.rpc.Status + ErrorInfo (reason — имя ErrorCode из proto/agent/v1/common.proto).
//
// Контракт — proto/agent/v1, сгенерированный код — server/gen/agent/v1. Здесь регистрируется только
// EnrollmentService; остальные сервисы (AgentService и т.д.) регистрирует вызывающий через register в NewServer.
package grpcserver
