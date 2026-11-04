package monitoring

// Оповещения о сбоях в Telegram (Этап 4, H2; решение Q-060). Laravel: app/Services/Monitoring/Alerts.php —
// оповещение уходит один раз, когда проблема началась, и один раз, когда она закончилась, а не каждую минуту,
// пока длится. Какие проблемы открыты — общее состояние инстансов: в старом — кэш, здесь — Valkey (SET NX / DEL —
// переход «нет → есть» и «есть → нет» атомарен, два инстанса не отправят одно оповещение дважды).

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// DefaultAlertPrefix — префикс ключей состояния ('monitoring.alert.'.$key).
const DefaultAlertPrefix = "zafiro:monitoring.alert."

// Text — текст оповещения: ключ перевода (русский текст старого кода) и подстановки. Язык — язык оповещений.
type Text struct {
	Key    string
	Params map[string]string
}

// Notifier — отправка оповещения на языке оповещений (*telegram.Alerts): false — бот не настроен или не отправил.
type Notifier interface {
	SendKey(ctx context.Context, prefix, key string, params map[string]string) (bool, error)
}

// Alerts — состояние открытых проблем и отправка оповещений.
type Alerts struct {
	Valkey valkey.Client
	// Prefix — префикс ключей (пусто — DefaultAlertPrefix).
	Prefix string
	// Bot — отправка (nil — состояние ведётся, оповещения не отправляются, как без настроенного бота).
	Bot Notifier
	Now func() time.Time
	Log *slog.Logger
}

func (a *Alerts) key(k string) string {
	p := a.Prefix
	if p == "" {
		p = DefaultAlertPrefix
	}
	return p + k
}

// Update — Alerts::update(): failing совпадает с состоянием — ничего; иначе состояние меняется и уходит
// «⚠️ проблема» или «✅ восстановлено». Возвращает, сменилось ли состояние.
func (a *Alerts) Update(ctx context.Context, key string, failing bool, problem, recovered Text) (bool, error) {
	if a == nil || a.Valkey == nil {
		return false, nil
	}
	changed := false
	if failing {
		now := time.Now
		if a.Now != nil {
			now = a.Now
		}
		// Cache::forever($cacheKey, now()->getTimestamp())
		err := a.Valkey.Do(ctx, a.Valkey.B().Set().Key(a.key(key)).Value(strconv.FormatInt(now().Unix(), 10)).Nx().Build()).Error()
		switch {
		case valkey.IsValkeyNil(err):
		case err != nil:
			return false, err
		default:
			changed = true
		}
	} else {
		n, err := a.Valkey.Do(ctx, a.Valkey.B().Del().Key(a.key(key)).Build()).AsInt64()
		if err != nil {
			return false, err
		}
		changed = n > 0
	}
	if !changed || a.Bot == nil {
		return changed, nil
	}
	t, prefix := recovered, "✅ "
	if failing {
		t, prefix = problem, "⚠️ "
	}
	if _, err := a.Bot.SendKey(ctx, prefix, t.Key, t.Params); err != nil && a.Log != nil {
		a.Log.WarnContext(ctx, "monitoring: alert not sent", "alert", key, "err", err)
	}
	return true, nil
}

// IsOpen — Alerts::isOpen().
func (a *Alerts) IsOpen(ctx context.Context, key string) (bool, error) {
	if a == nil || a.Valkey == nil {
		return false, nil
	}
	n, err := a.Valkey.Do(ctx, a.Valkey.B().Exists().Key(a.key(key)).Build()).AsInt64()
	return n > 0, err
}
