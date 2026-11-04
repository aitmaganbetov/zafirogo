package guestapi

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// Язык текстов ошибок (open-questions/stage5-agent.md AG-16): google.rpc.LocalizedMessage переводится на язык из
// метаданных `zafiro-locale` (служба агента передаёт язык, выбранный гостем на экране; старый агент слал
// Accept-Language языка гостя — L.SetUserLocale), если он включён на платформе, иначе — на язык клуба ПК
// (LocaleResolver::forClub). Ключ перевода — русский текст, как в старом коде (`__()`); подстановки :name
// переводятся вместе с ключом, поэтому rpcError кладёт ключ и параметры во внутреннюю деталь (i18nMarker), которую
// перехватчик убирает из ответа.

// i18nMarker — префикс DebugInfo.detail с ключом перевода (деталь только внутри сервера).
const i18nMarker = "zafiro-i18n:"

// Translator — каталог переводов (*i18n.Catalog).
type Translator interface {
	Enabled(ctx context.Context) ([]i18n.Language, string, error)
	ClubLocale(ctx context.Context, clubID int64) string
	Translate(ctx context.Context, locale, key string, params map[string]string) string
}

// withI18nKey — ошибка gRPC + ключ перевода и параметры (для перехватчика LocalizeErrors).
func withI18nKey(err error, key string, params map[string]string) error {
	st, ok := status.FromError(err)
	if !ok || len(params) == 0 {
		return err
	}
	entries := make([]string, 0, len(params))
	for k, v := range params {
		entries = append(entries, k+"="+v)
	}
	slices.Sort(entries)
	withD, derr := st.WithDetails(&errdetails.DebugInfo{Detail: i18nMarker + key, StackEntries: entries})
	if derr != nil {
		return err
	}
	return withD.Err()
}

// LocalizeErrors — перехватчик unary-RPC агента: текст ошибки на языке гостя или клуба. t == nil — тексты остаются
// русскими, внутренняя деталь всё равно убирается.
func LocalizeErrors(t Translator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		return resp, localize(ctx, t, err)
	}
}

// requestLocale — язык запроса: zafiro-locale, если включён; иначе язык клуба ПК; без клуба — "".
func requestLocale(ctx context.Context, t Translator) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("zafiro-locale"); len(v) > 0 && localeRe.MatchString(v[0]) {
		if langs, _, err := t.Enabled(ctx); err == nil &&
			slices.ContainsFunc(langs, func(l i18n.Language) bool { return l.Code == v[0] }) {
			return v[0]
		}
	}
	if id, ok := identity.FromContext(ctx); ok && id.ClubID > 0 {
		return t.ClubLocale(ctx, id.ClubID)
	}
	return ""
}

func localize(ctx context.Context, t Translator, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	details := st.Details()
	var key string
	var params map[string]string
	hasMarker, hasMessage := false, false
	for _, d := range details {
		switch v := d.(type) {
		case *errdetails.DebugInfo:
			if strings.HasPrefix(v.GetDetail(), i18nMarker) {
				hasMarker = true
				key = strings.TrimPrefix(v.GetDetail(), i18nMarker)
				params = map[string]string{}
				for _, e := range v.GetStackEntries() {
					if k, val, ok := strings.Cut(e, "="); ok {
						params[k] = val
					}
				}
			}
		case *errdetails.LocalizedMessage:
			hasMessage = true
			if key == "" {
				key = v.GetMessage()
			}
		}
	}
	if !hasMarker && (!hasMessage || t == nil) {
		return err
	}
	locale := ""
	if t != nil && hasMessage {
		locale = requestLocale(ctx, t)
	}
	original := st.Message()
	text := original
	if locale != "" && key != "" {
		text = t.Translate(ctx, locale, key, params)
	}
	out := make([]protoadapt.MessageV1, 0, len(details))
	for _, d := range details {
		switch v := d.(type) {
		case *errdetails.DebugInfo:
			if strings.HasPrefix(v.GetDetail(), i18nMarker) {
				continue
			}
		case *errdetails.LocalizedMessage:
			if locale != "" {
				d = &errdetails.LocalizedMessage{Locale: locale, Message: text}
			}
		case *errdetails.BadRequest:
			for _, fv := range v.GetFieldViolations() {
				if fv.GetDescription() == original {
					fv.Description = text
				}
			}
		}
		if m, ok := d.(protoadapt.MessageV1); ok {
			out = append(out, m)
		}
	}
	ns, derr := status.New(st.Code(), text).WithDetails(out...)
	if derr != nil {
		return err
	}
	return ns.Err()
}
