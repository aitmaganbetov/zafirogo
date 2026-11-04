package guestapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

// Идемпотентность денежных RPC (agent.proto п. 5, docs/agent-protocol.md §5): (computer_id, idempotency_key) →
// отпечаток запроса + ответ, 24 часа (как /api/v1: app/Http/Middleware/Api/ApiIdempotency.php):
//   - тот же ключ и тот же запрос — сохранённый ответ (или та же бизнес-ошибка), действие не повторяется;
//   - тот же ключ, другой запрос — ERROR_CODE_IDEMPOTENCY_KEY_REUSED;
//   - запрос ещё выполняется — ERROR_CODE_IDEMPOTENCY_IN_PROGRESS (ABORTED);
//   - INTERNAL/UNAVAILABLE/отмена не сохраняются (можно повторить).
//
// Второй рубеж — ключ в БД (billing_requests, ledger_idempotency): тот же ключ с префиксом ПК передаётся сервису,
// поэтому потеря Valkey не приводит к двойному списанию.

// uuidRe — UUID любой версии (агент генерирует v4/v7).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// inProgressTTL — сколько держится отметка «выполняется» (дольше любого deadline unary-вызова, 15 с).
const inProgressTTL = 60 * time.Second

type idempotency struct {
	c      valkey.Client
	prefix string
	ttl    time.Duration
}

func idemRequired() error {
	return rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REQUIRED, msgIdemRequired, nil, "idempotency_key")
}

// serviceKey — ключ идемпотентности для сервисов с хранением в БД: уникален в клубе (ключ — от агента,
// UUID; префикс ПК исключает совпадение ключей двух ПК).
func serviceKey(computerID int64, key string) string {
	return "agent:" + strconv.FormatInt(computerID, 10) + ":" + strings.ToLower(key)
}

// once выполняет fn не больше одного раза на (ПК, ключ). out заполняется ответом fn или сохранённым.
func once[T proto.Message](ctx context.Context, i *idempotency, computerID int64, method, key string, req proto.Message, out T, fn func() (T, error)) (T, error) {
	var zero T
	if !uuidRe.MatchString(key) {
		return zero, idemRequired()
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(append([]byte(method+"\x00"), body...))
	fp := hex.EncodeToString(sum[:16])
	k := i.prefix + strconv.FormatInt(computerID, 10) + ":" + strings.ToLower(key)

	ok, err := i.c.Do(ctx, i.c.B().Set().Key(k).Value("p|"+fp).Nx().Px(inProgressTTL).Build()).AsBool()
	if err != nil && !valkey.IsValkeyNil(err) {
		return zero, err
	}
	if !ok {
		return replay(ctx, i, k, fp, out)
	}

	res, ferr := fn()
	if ferr != nil {
		st, _ := status.FromError(ferr)
		if !storable(st.Code()) {
			_ = i.c.Do(context.WithoutCancel(ctx), i.c.B().Del().Key(k).Build()).Error()
			return zero, ferr
		}
		b, merr := proto.Marshal(st.Proto())
		if merr == nil {
			_ = i.c.Do(context.WithoutCancel(ctx), i.c.B().Set().Key(k).Value("e|"+fp+"|"+base64.StdEncoding.EncodeToString(b)).Px(i.ttl).Build()).Error()
		}
		return zero, ferr
	}
	b, err := proto.Marshal(res)
	if err == nil {
		err = i.c.Do(context.WithoutCancel(ctx), i.c.B().Set().Key(k).Value("d|"+fp+"|"+base64.StdEncoding.EncodeToString(b)).Px(i.ttl).Build()).Error()
	}
	if err != nil {
		// Действие уже выполнено; повтор упрётся в ключ БД сервиса. Отметку «выполняется» снимаем.
		_ = i.c.Do(context.WithoutCancel(ctx), i.c.B().Del().Key(k).Build()).Error()
	}
	return res, nil
}

// storable — какие ошибки запоминаются за ключом (бизнес-ответы, как 4xx в ApiIdempotency).
func storable(c codes.Code) bool {
	switch c {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied, codes.AlreadyExists, codes.OutOfRange:
		return true
	}
	return false
}

func replay[T proto.Message](ctx context.Context, i *idempotency, k, fp string, out T) (T, error) {
	var zero T
	v, err := i.c.Do(ctx, i.c.B().Get().Key(k).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		// Отметка снята между SET NX и GET (первый запрос упал с INTERNAL) — можно повторить.
		return zero, rpcError(codes.Aborted, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_IN_PROGRESS, msgIdemInProgress, nil, "")
	}
	if err != nil {
		return zero, err
	}
	parts := strings.SplitN(v, "|", 3)
	if len(parts) < 2 || parts[1] != fp {
		return zero, rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_KEY_REUSED, msgIdemReused, nil, "idempotency_key")
	}
	switch parts[0] {
	case "d":
		if len(parts) == 3 {
			if b, err := base64.StdEncoding.DecodeString(parts[2]); err == nil && proto.Unmarshal(b, out) == nil {
				return out, nil
			}
		}
	case "e":
		if len(parts) == 3 {
			if b, err := base64.StdEncoding.DecodeString(parts[2]); err == nil {
				var sp spb.Status
				if proto.Unmarshal(b, &sp) == nil {
					return zero, status.FromProto(&sp).Err()
				}
			}
		}
	}
	return zero, rpcError(codes.Aborted, agentv1.ErrorCode_ERROR_CODE_IDEMPOTENCY_IN_PROGRESS, msgIdemInProgress, nil, "")
}
