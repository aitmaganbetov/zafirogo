package superadmin

// Релиз агента ПК (platform_settings 'agent_release'). Laravel: app/Http/Controllers/Superadmin/AgentReleaseController.php,
// app/Services/AgentUpdate.php.
//
// Отличие от старого (решение «Обновления агента», docs/DECISIONS.md): автообновление включается только с
// подписью Ed25519 от CI. Старый код брал контрольную сумму из файла <установщик>.sha256 на GitHub; новый берёт
// манифест <установщик>.update.json (sha256, size_bytes, channel, key_id, signature_ed25519 — его публикует CI
// рядом с установщиком) и проверяет подпись открытым ключом сервера (commands.VerifyUpdate). ПК со старой версией
// агента получают UpdateAvailable (долговременная команда, 24 ч) — docs/open-questions/stage3-superadmin.md SA-3.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	settingAgentRelease = "agent_release"
	// installerURL — AgentUpdate::INSTALLER_URL (установщик, собранный CI .github/workflows/windows-agent.yml).
	installerURL = "https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v{version}/ZafiroAgentSetup-{version}.exe"
	// manifestSuffix — манифест подписи рядом с установщиком.
	manifestSuffix = ".update.json"
	subjectRelease = `App\Models\PlatformSetting`
)

var (
	versionRe       = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,6}$`)
	latestVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	versionPrefixRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)
)

// release — значение platform_settings 'agent_release'.
type release struct {
	LatestVersion   string  `json:"latest_version"`
	ReleasedAt      string  `json:"released_at"`
	DownloadURL     *string `json:"download_url"`
	AutoUpdate      bool    `json:"auto_update"`
	InstallerURL    *string `json:"installer_url"`
	InstallerSHA256 *string `json:"installer_sha256"`
	// Подписанное обновление (новое): размер, канал, ключ и подпись CI.
	InstallerSize      int64  `json:"installer_size,omitempty"`
	InstallerChannel   string `json:"installer_channel,omitempty"`
	InstallerKeyID     string `json:"installer_key_id,omitempty"`
	InstallerSignature string `json:"installer_signature,omitempty"`

	raw json.RawMessage
}

func readRelease(ctx context.Context, q *store.Queries) (release, error) {
	raw, err := q.SuperadminSetting(ctx, settingAgentRelease)
	if errors.Is(err, pgx.ErrNoRows) || len(raw) == 0 || string(raw) == "null" {
		return release{}, nil
	}
	if err != nil {
		return release{}, err
	}
	var r release
	if json.Unmarshal(raw, &r) != nil {
		return release{}, nil
	}
	r.raw = raw
	return r, nil
}

// latestVersion — AgentUpdate::latestVersion(): только «x.y.z».
func (r release) latestVersion() string {
	if latestVersionRe.MatchString(r.LatestVersion) {
		return r.LatestVersion
	}
	return ""
}

// downloadURL — AgentUpdate::downloadUrl(): только https-ссылка.
func (r release) downloadURL() *string {
	if r.DownloadURL == nil || !strings.HasPrefix(*r.DownloadURL, "https://") {
		return nil
	}
	if u, err := url.Parse(*r.DownloadURL); err != nil || u.Host == "" {
		return nil
	}
	v := *r.DownloadURL
	return &v
}

// isOutdated — AgentUpdate::isOutdated(): нечитаемая версия считается старой; сравнение — как version_compare.
func isOutdated(version, latest string) bool {
	if latest == "" {
		return false
	}
	m := versionPrefixRe.FindStringSubmatch(version)
	l := versionPrefixRe.FindStringSubmatch(latest)
	if m == nil || l == nil {
		return m == nil
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.ParseInt(m[i], 10, 64)
		b, _ := strconv.ParseInt(l[i], 10, 64)
		if a != b {
			return a < b
		}
	}
	return false
}

// manifest — <установщик>.update.json от CI.
type manifest struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Channel   string `json:"channel"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature_ed25519"`
}

func (h *handlers) httpClient() *http.Client {
	if h.d.Superadmin.HTTPClient != nil {
		return h.d.Superadmin.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// signedUpdate — манифест подписи установщика версии и проверка подписи; ошибка — текст поля auto_update.
func (h *handlers) signedUpdate(ctx context.Context, version, installer string) (*agentv1.UpdateAvailable, error) {
	fail := httpx.ValidationMsg("auto_update",
		"Не удалось получить контрольную сумму установщика :url. Проверьте, что сборка агента этой версии опубликована на GitHub.",
		map[string]string{"url": installer})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installer+manifestSuffix, nil)
	if err != nil {
		return nil, fail
	}
	res, err := h.httpClient().Do(req) // #nosec G107 -- адрес из шаблона INSTALLER_URL (GitHub), не от пользователя
	if err != nil {
		return nil, fail
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	var m manifest
	if err != nil || res.StatusCode != http.StatusOK || json.Unmarshal(body, &m) != nil || m.Version != version {
		return nil, fail
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return nil, fail
	}
	u := &agentv1.UpdateAvailable{Version: version, Url: installer, Sha256: strings.ToLower(m.SHA256), SizeBytes: m.SizeBytes,
		SignatureEd25519: sig, KeyId: m.KeyID, Channel: m.Channel}
	if u.Channel == "" {
		u.Channel = "stable"
	}
	if err := commands.VerifyUpdate(u, h.d.Superadmin.UpdateKeys); err != nil {
		h.d.Log().WarnContext(ctx, "agent release: signature check failed", "version", version, "err", err)
		return nil, httpx.ValidationMsg("auto_update", "Подпись установщика :url не прошла проверку.", map[string]string{"url": installer})
	}
	return u, nil
}

// SaSetAgentRelease — AgentReleaseController::update().
func (h *handlers) SaSetAgentRelease(ctx context.Context, req superadminapi.SaSetAgentReleaseRequestObject) (superadminapi.SaSetAgentReleaseResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("latest_version", "required")
	}
	b := *req.Body
	var fe httpx.FormErrors
	version := strings.TrimSpace(b.LatestVersion)
	switch {
	case version == "":
		fe.Add("latest_version", httpx.RuleMessage("latest_version", "required", ""))
	case !versionRe.MatchString(version):
		fe.AddMsg("latest_version", "Версия — три числа через точку, например 1.2.0.", nil)
	}
	releasedAt := strings.TrimSpace(b.ReleasedAt)
	if releasedAt == "" {
		fe.Add("released_at", httpx.RuleMessage("released_at", "required", ""))
	} else if _, err := time.Parse("2006-01-02", releasedAt); err != nil {
		if _, err := time.Parse(time.RFC3339, releasedAt); err != nil {
			fe.Add("released_at", httpx.RuleMessage("released_at", "date", ""))
		}
	}
	var download *string
	if b.DownloadUrl != nil && strings.TrimSpace(*b.DownloadUrl) != "" {
		v := strings.TrimSpace(*b.DownloadUrl)
		u, err := url.Parse(v)
		switch {
		case len([]rune(v)) > 500:
			fe.Add("download_url", httpx.RuleMessage("download_url", "max_string", "500"))
		case err != nil || u.Scheme != "https" || u.Host == "":
			fe.AddMsg("download_url", "Ссылка на скачивание должна начинаться с https://.", nil)
		}
		download = &v
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	auto := b.AutoUpdate != nil && *b.AutoUpdate
	rel := release{LatestVersion: version, ReleasedAt: releasedAt, DownloadURL: download, AutoUpdate: auto}
	var update *agentv1.UpdateAvailable
	if auto {
		inst := strings.ReplaceAll(installerURL, "{version}", version)
		u, err := h.signedUpdate(ctx, version, inst)
		if err != nil {
			return nil, err
		}
		update = u
		rel.InstallerURL, rel.InstallerSHA256 = &inst, &u.Sha256
		rel.InstallerSize, rel.InstallerChannel, rel.InstallerKeyID = u.SizeBytes, u.Channel, u.KeyId
		rel.InstallerSignature = base64.StdEncoding.EncodeToString(u.SignatureEd25519)
	}
	raw, err := json.Marshal(rel)
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	var targets [][2]int64
	err = h.tx(ctx, func(q *store.Queries) error {
		before, err := q.SuperadminSetting(ctx, settingAgentRelease)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		uid := httpx.Principal(ctx).UserID
		if err := q.SuperadminPutSetting(ctx, store.SuperadminPutSettingParams{Key: settingAgentRelease, Value: raw, UpdatedBy: &uid, Now: &now}); err != nil {
			return err
		}
		details := map[string]any{"latest_version": version, "released_at": releasedAt, "download_url": download, "auto_update": auto}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.agent_release_set", Key: "Объявлена версия агента :version",
			Params: map[string]string{"version": version}, Details: details}); err != nil {
			return err
		}
		if err := h.audit(ctx, q, 0, "settings", "agent_release.set", subjectRelease, 0, json.RawMessage(nonEmpty(before)), json.RawMessage(raw)); err != nil {
			return err
		}
		if update == nil {
			return nil
		}
		rows, err := q.SuperadminAgentReleaseTargets(ctx, &now)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if isOutdated(r.AgentVersion, version) && clubAllowsAutoUpdate(r.AutoUpdate) {
				targets = append(targets, [2]int64{r.ClubID, r.ID})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if update != nil && len(targets) > 0 {
		h.sendUpdate(ctx, update, targets)
	}
	return superadminapi.SaSetAgentRelease200JSONResponse(msg(h.d.T(ctx,
		"Версия агента :version объявлена: клубы со старыми агентами получат уведомление.", map[string]string{"version": version}))), nil
}

// clubAllowsAutoUpdate — AgentUpdate::clubAllowsAutoUpdate(): filter_var(… ?? true, FILTER_VALIDATE_BOOLEAN).
func clubAllowsAutoUpdate(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "1", "true", "on", "yes":
		return true
	}
	return false
}

func nonEmpty(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// sendUpdate — UpdateAvailable ПК со старым агентом (в фоне: долговременная команда ждёт ПК не в сети 24 ч).
func (h *handlers) sendUpdate(ctx context.Context, u *agentv1.UpdateAvailable, targets [][2]int64) {
	if h.d.AgentCommands == nil {
		return
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_UpdateAvailable{UpdateAvailable: u}}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		for _, t := range targets {
			if _, err := h.d.AgentCommands.Send(ctx, t[0], t[1], cmd); err != nil {
				h.d.Log().WarnContext(ctx, "agent release: send", "club_id", t[0], "computer_id", t[1], "err", err)
			}
		}
	}()
}

// SaClearAgentRelease — AgentReleaseController::destroy().
func (h *handlers) SaClearAgentRelease(ctx context.Context, _ superadminapi.SaClearAgentReleaseRequestObject) (superadminapi.SaClearAgentReleaseResponseObject, error) {
	err := h.tx(ctx, func(q *store.Queries) error {
		before, err := q.SuperadminSetting(ctx, settingAgentRelease)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := q.SuperadminDeleteSetting(ctx, settingAgentRelease); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.agent_release_cleared", Key: "Объявление о версии агента снято"}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "agent_release.cleared", subjectRelease, 0, json.RawMessage(nonEmpty(before)), nil)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaClearAgentRelease200JSONResponse(msg(h.d.T(ctx, "Объявление снято.", nil))), nil
}
