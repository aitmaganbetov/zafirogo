package guestapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// MaxImageBytes — больше картинка не отдаётся (club_images: логотип ≤ 1 МБ, картинка входа ≤ 3 МБ; news_images ≤ 2 МБ).
const MaxImageBytes = 3 << 20

// GetImage — картинка клуба для UI агента (open-questions/stage5-agent.md AG-13): логотип и картинка экрана входа
// клуба ПК (ClubLogoController::show, ClubSignInImageController::show), картинка новости клуба ПК или платформы
// (NewsController::image). Клуб — только из сертификата, в отличие от публичных URL старого агента. Лимит — как у
// переводов (agent-translations, 600/мин на IP): та же статика интерфейса, служба кэширует по etag.
func (a *AgentService) GetImage(ctx context.Context, req *agentv1.GetImageRequest) (*agentv1.GetImageResponse, error) {
	s := a.g
	ctx, id, err := s.call(ctx, ratelimit.AgentTranslations, nil)
	if err != nil {
		return nil, err
	}
	var contentType string
	var data []byte
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		switch req.GetKind() {
		case agentv1.ImageKind_IMAGE_KIND_CLUB_LOGO, agentv1.ImageKind_IMAGE_KIND_SIGNIN:
			kind, path := "logo", func(r store.SettingsPublicImageRow) *string { return r.LogoPath }
			if req.GetKind() == agentv1.ImageKind_IMAGE_KIND_SIGNIN {
				kind, path = "signin", func(r store.SettingsPublicImageRow) *string { return r.SigninImagePath }
			}
			row, err := q.SettingsPublicImage(ctx, store.SettingsPublicImageParams{ClubID: id.ClubID, Kind: kind})
			if err != nil {
				return err
			}
			if p := path(row); p == nil || *p == "" { // картинка удалена в настройках
				return pgx.ErrNoRows
			}
			contentType, data = row.ContentType, row.Data
		case agentv1.ImageKind_IMAGE_KIND_NEWS:
			row, err := q.PublicNewsImage(ctx, req.GetId())
			if err != nil {
				return err
			}
			if (row.ClubID != nil && *row.ClubID != id.ClubID) || row.ImagePath == nil || *row.ImagePath == "" {
				return pgx.ErrNoRows
			}
			contentType, data = row.ContentType, row.Data
		default:
			return validation("kind", msgNotFound) // вид картинки не задан или неизвестен
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (len(data) == 0 || len(data) > MaxImageBytes ||
		(contentType != "image/png" && contentType != "image/jpeg"))) {
		return nil, rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED, msgNotFound, nil, "id")
	}
	if err != nil {
		return nil, s.fail(ctx, "GetImage", err)
	}
	sum := sha256.Sum256(data)
	etag := hex.EncodeToString(sum[:])
	if req.GetIfNoneMatch() == etag {
		return &agentv1.GetImageResponse{NotModified: true, ContentType: contentType, Etag: etag}, nil
	}
	return &agentv1.GetImageResponse{Data: data, ContentType: contentType, Etag: etag}, nil
}
