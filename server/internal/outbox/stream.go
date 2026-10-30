package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// StreamOptions — параметры потока zafiro-events.
type StreamOptions struct {
	// Name — имя потока (пусто — StreamName).
	Name string
	// MaxAge — сколько хранить события (0 — 72 ч). Получатели, отставшие сильнее, теряют события:
	// админка перечитывает состояние при переподключении, вебхуки создают доставки в Postgres сразу.
	MaxAge time.Duration
	// Duplicates — окно дедупликации по Nats-Msg-Id (0 — 10 мин). Должно быть больше, чем публикатор может
	// «висеть» между отправкой и пометкой published_at (батч + ретраи).
	Duplicates time.Duration
	// Replicas — число реплик (0 — 1; в кластере NATS — 3).
	Replicas int
	// MaxBytes — предел размера потока (0 — без предела, только MaxAge).
	MaxBytes int64
}

// EnsureStream создаёт или обновляет поток событий клубов (subjects club.>, файловое хранение).
func EnsureStream(ctx context.Context, js jetstream.JetStream, o StreamOptions) (jetstream.Stream, error) {
	cfg := jetstream.StreamConfig{
		Name:        o.Name,
		Description: "Zafiro: события клубов из outbox (club.<club_id>.<type>)",
		Subjects:    []string{AllSubjects},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      o.MaxAge,
		Duplicates:  o.Duplicates,
		Replicas:    o.Replicas,
		MaxBytes:    o.MaxBytes,
		Discard:     jetstream.DiscardOld,
	}
	if cfg.Name == "" {
		cfg.Name = StreamName
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 72 * time.Hour
	}
	if cfg.Duplicates <= 0 {
		cfg.Duplicates = 10 * time.Minute
	}
	if cfg.Replicas <= 0 {
		cfg.Replicas = 1
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = -1
	}
	s, err := js.CreateOrUpdateStream(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("outbox: ensure stream %s: %w", cfg.Name, err)
	}
	return s, nil
}
