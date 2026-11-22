package settings

// Город и адрес клуба, профиль клуба для гостевого приложения, фото, оборудование зон (тема В,
// docs/upstream-2026-11.md). Laravel: SettingsController::location(),
// app/Http/Controllers/ClubProfileController.php. Город, адрес, профиль — колонки clubs, оборудование —
// club_zones.devices, фото — club_photos + club_photo_images (байты в базе, решение UQ-19).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/catalog"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты SettingsController::location() и ClubProfileController (ключи перевода).
const (
	msgLocationSaved = "Город и адрес сохранены."
	msgProfileSaved  = "Описание клуба сохранено."
	msgPhotosAdded   = "Фото добавлены."
	msgPhotoDeleted  = "Фото удалено."
	msgCoverChanged  = "Обложка клуба изменена."
	msgZoneSaved     = "Зона :zone сохранена."
	msgPhotoMax      = "Фото — не больше 3 МБ."
	msgPhotoLimit    = "У клуба может быть не больше :count фото."

	logLocation    = "Город и адрес клуба изменены: :location"
	logProfile     = "Описание клуба для приложения изменено"
	logPhotosAdded = "Добавлено :count фото клуба|Добавлено :count фото клуба|Добавлено :count фото клуба"
	logPhotoDelete = "Фото клуба удалено"
	logZoneDevices = "Оборудование зоны :zone изменено"

	subjectZone = `App\Models\ClubZone`

	// photoDir — каталог пути фото (storeAs('club-photos/<club>', …)) и публичный маршрут.
	photoDir   = "club-photos"
	photoRoute = "/club-photos/"
	// photoMaxBytes — max:3072 (КБ); photoMin, photoMax — dimensions:min_width=200,…,max_width=6000,….
	photoMaxBytes = 3072 * 1024
	photoMin      = 200
	photoMax      = 6000
)

// photoFileRe — имя файла публичного адреса: ->where('file', '[A-Za-z0-9]{40}\.(png|jpg|webp)').
var photoFileRe = regexp.MustCompile(`^[A-Za-z0-9]{40}\.(png|jpg|webp)$`)

// photoURL — ClubPhoto::url(): route('club-photos.show', [$club_id, basename($path)]).
func photoURL(clubID int64, p string) string {
	return photoRoute + strconv.FormatInt(clubID, 10) + "/" + path.Base(p)
}

// trimmed — значение поля формы после TrimStrings и ConvertEmptyStringsToNull ("" — поле пустое).
func trimmed(v *string) string {
	if v == nil {
		return ""
	}
	return catalog.Trim(*v)
}

func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// cityOptions — Cities::options(): города по названию на языке клуба.
func (h *handlers) cityOptions(ctx context.Context) []settingsapi.CityOption {
	opts := catalog.CityOptions(func(name string) string { return h.d.T(ctx, name, nil) })
	out := make([]settingsapi.CityOption, len(opts))
	for i, o := range opts {
		out[i] = settingsapi.CityOption{Key: o.Key, Name: o.Label}
	}
	return out
}

// SaveClubLocation — SettingsController::location(): город из списка и адрес; журнал — только при изменении.
func (h *handlers) SaveClubLocation(ctx context.Context, req settingsapi.SaveClubLocationRequestObject) (settingsapi.SaveClubLocationResponseObject, error) {
	var city, address string
	if req.Body != nil {
		city, address = trimmed(req.Body.City), trimmed(req.Body.Address)
	}
	var fe httpx.FormErrors
	if _, ok := catalog.CityByKey(city); city != "" && !ok {
		fe.Rule("city", cash.RuleIn, "")
	}
	if utf8.RuneCountInString(address) > catalog.MaxAddress {
		fe.Rule("address", "max_string", strconv.Itoa(catalog.MaxAddress))
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.ClubProfileLock(ctx, p.ClubID)
		if err != nil {
			return err
		}
		// $club->isDirty(['city', 'address']).
		if deref(row.City) == city && deref(row.Address) == address && (row.City == nil) == (city == "") && (row.Address == nil) == (address == "") {
			return nil
		}
		if err := q.ClubProfileSetLocation(ctx, store.ClubProfileSetLocationParams{ClubID: p.ClubID, City: nullable(city),
			Address: nullable(address), Now: &now}); err != nil {
			return err
		}
		// collect([Cities::name($club->city), $club->address])->filter()->implode(', ') ?: '—'.
		var parts []string
		if c, ok := catalog.CityByKey(city); ok {
			parts = append(parts, c.Name)
		}
		if address != "" && address != "0" { // filter() PHP отбрасывает и строку "0"
			parts = append(parts, address)
		}
		location := strings.Join(parts, ", ")
		if location == "" {
			location = "—"
		}
		return logClub(ctx, q, now, "club.location_changed", nil, logLocation, map[string]string{"location": location})
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.SaveClubLocation200JSONResponse{Message: h.d.T(ctx, msgLocationSaved, nil)}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseProfile — clubs.profile → профиль (NULL или не объект — пустой).
func parseProfile(raw json.RawMessage) catalog.Profile {
	var p catalog.Profile
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

// parseDevices — club_zones.devices → значения по ключам (строки; прочее игнорируется).
func parseDevices(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// GetClubProfile — ClubProfileController::show().
func (h *handlers) GetClubProfile(ctx context.Context, _ settingsapi.GetClubProfileRequestObject) (settingsapi.GetClubProfileResponseObject, error) {
	p := httpx.Principal(ctx)
	var (
		club   store.ClubProfileGetRow
		photos []store.ClubProfilePhotosRow
		zones  []store.ClubProfileZonesRow
	)
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if club, err = q.ClubProfileGet(ctx, p.ClubID); err != nil {
			return err
		}
		if photos, err = q.ClubProfilePhotos(ctx, p.ClubID); err != nil {
			return err
		}
		zones, err = q.ClubProfileZones(ctx, p.ClubID)
		return err
	})
	if err != nil {
		return nil, err
	}
	prof := parseProfile(club.Profile)
	out := settingsapi.ClubProfilePage{
		Profile: settingsapi.ClubProfile{Description: nullable(prof.Description), Phone: nullable(prof.Phone),
			Hours: nullable(prof.Hours), Instagram: nullable(prof.Instagram)},
		Photos:       make([]settingsapi.ClubPhoto, 0, len(photos)),
		PhotoLimit:   catalog.PhotoLimit,
		Zones:        make([]settingsapi.ZoneProfile, 0, len(zones)),
		DeviceFields: make([]settingsapi.DeviceField, 0, 8),
	}
	for _, ph := range photos {
		out.Photos = append(out.Photos, settingsapi.ClubPhoto{Id: ph.ID, Url: photoURL(p.ClubID, ph.Path)})
	}
	tr := func(key string, params map[string]string) string { return h.d.T(ctx, key, params) }
	for _, z := range zones {
		d := catalog.Detected{CPU: z.CpuModel, HasCPU: z.HasCpu, GPU: z.GpuModel, HasGPU: z.HasGpu, RAMMegabytes: z.RamTotalMb, HasRAM: z.HasRam}
		out.Zones = append(out.Zones, settingsapi.ZoneProfile{Id: z.ID, Name: z.Name, Description: z.Description,
			ComputersCount: z.ComputersCount, PricePerHour: z.PricePerHour, Devices: parseDevices(z.Devices), Detected: d.Values(tr)})
	}
	for _, f := range catalog.DeviceFields() {
		out.DeviceFields = append(out.DeviceFields, settingsapi.DeviceField{Key: f.Key, Label: h.d.T(ctx, f.Label, nil)})
	}
	return settingsapi.GetClubProfile200JSONResponse(out), nil
}

// SaveClubProfile — ClubProfileController::update().
func (h *handlers) SaveClubProfile(ctx context.Context, req settingsapi.SaveClubProfileRequestObject) (settingsapi.SaveClubProfileResponseObject, error) {
	var b settingsapi.ClubProfileRequest
	if req.Body != nil {
		b = *req.Body
	}
	prof, errs := catalog.NewProfile(trimmed(b.Description), trimmed(b.Phone), trimmed(b.Hours), trimmed(b.Instagram))
	var fe httpx.FormErrors
	for _, e := range errs {
		if e.Rule == "max" {
			fe.Rule(e.Field, "max_string", strconv.Itoa(e.Max))
		} else {
			fe.Add(e.Field, e.Message)
		}
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	var doc json.RawMessage
	if !prof.Empty() {
		var err error
		if doc, err = json.Marshal(prof); err != nil {
			return nil, err
		}
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if _, err := q.ClubProfileLock(ctx, p.ClubID); err != nil {
			return err
		}
		if err := q.ClubProfileSetProfile(ctx, store.ClubProfileSetProfileParams{ClubID: p.ClubID, Profile: doc, Now: &now}); err != nil {
			return err
		}
		return logClub(ctx, q, now, "club.profile_changed", nil, logProfile, nil)
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.SaveClubProfile200JSONResponse{Message: h.d.T(ctx, msgProfileSaved, nil)}, nil
}

// ---------------------------------------------------------------------------
// Фото
// ---------------------------------------------------------------------------

// photo — загруженное фото после проверки.
type photo struct {
	data        []byte
	contentType string
	ext         string
}

// webpSize — размеры WebP по заголовку (декодера WebP в стандартной библиотеке нет; getimagesize() PHP тоже
// читает только заголовок): контейнер RIFF/WEBP, первый блок — VP8 (с потерями), VP8L (без потерь) или VP8X.
func webpSize(b []byte) (w, h int, ok bool) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8 ":
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, false
		}
		v := binary.LittleEndian.Uint32(b[21:25])
		return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1, true
	case "VP8X":
		return int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1, int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1, true
	}
	return 0, 0, false
}

// sniffPhoto — тип и размеры картинки по содержимому: PNG, JPEG или WebP (правило mimes судит по содержимому
// файла, не по имени). format — "" (не картинка этих типов).
func sniffPhoto(data []byte) (format string, w, h int) {
	if ww, hh, ok := webpSize(data); ok {
		return "webp", ww, hh
	}
	cfg, f, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (f != "png" && f != "jpeg") {
		return "", 0, 0
	}
	return f, cfg.Width, cfg.Height
}

// readPhotos — файлы формы photos[] и их проверка:
// 'photos' => required|array|max:10; 'photos.*' => file|mimes:jpg,jpeg,png,webp|max:3072|dimensions:200…6000.
func readPhotos(mr *multipart.Reader) ([]photo, error) {
	var (
		out []photo
		fe  httpx.FormErrors
	)
	for i := 0; mr != nil; {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		name := part.FormName()
		if (name != "photos" && !strings.HasPrefix(name, "photos[")) || part.FileName() == "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 64*1024))
			continue
		}
		if i >= catalog.PhotoLimit {
			// max:10 — лишние файлы не читаем.
			return nil, httpx.RuleErrorWith("photos", "max_array", strconv.Itoa(catalog.PhotoLimit))
		}
		data, err := io.ReadAll(io.LimitReader(part, photoMaxBytes+1))
		if err != nil {
			return nil, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		field := "photos." + strconv.Itoa(i)
		i++
		format, w, h := sniffPhoto(data)
		if format == "" {
			// validation.mimes и validation.dimensions — в ru.php нет: английские тексты валидатора, как в старом.
			fe.Add(field, fmt.Sprintf("The %s field must be a file of type: jpg, jpeg, png, webp.", field))
		}
		if len(data) > photoMaxBytes {
			fe.Add(field, msgPhotoMax)
		}
		if format == "" || w < photoMin || h < photoMin || w > photoMax || h > photoMax {
			fe.Add(field, fmt.Sprintf("The %s field has invalid image dimensions.", field))
		}
		// guessExtension(): png → png, webp → webp, иначе jpg.
		ph := photo{data: data, contentType: "image/jpeg", ext: "jpg"}
		switch format {
		case "png":
			ph.contentType, ph.ext = "image/png", "png"
		case "webp":
			ph.contentType, ph.ext = "image/webp", "webp"
		}
		out = append(out, ph)
	}
	if len(out) == 0 {
		return nil, httpx.RuleError("photos", cash.RuleRequired)
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UploadClubPhotos — ClubProfileController::storePhotos(): до 10 фото у клуба, новые — в конец.
func (h *handlers) UploadClubPhotos(ctx context.Context, req settingsapi.UploadClubPhotosRequestObject) (settingsapi.UploadClubPhotosResponseObject, error) {
	photos, err := readPhotos(req.Body)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(photos))
	for i := range photos {
		if names[i], err = httpx.RandomName(); err != nil {
			return nil, err
		}
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		// Блокировка строки клуба: счёт и вставка — под ней (параллельные загрузки не превысят лимит).
		if _, err := q.ClubProfileLock(ctx, p.ClubID); err != nil {
			return err
		}
		st, err := q.ClubProfilePhotoStats(ctx, p.ClubID)
		if err != nil {
			return err
		}
		if st.Total+int64(len(photos)) > catalog.PhotoLimit {
			return httpx.ValidationMsg("photos", msgPhotoLimit, map[string]string{"count": strconv.Itoa(catalog.PhotoLimit)})
		}
		order := st.MaxOrder
		for i, ph := range photos {
			order++
			id, err := q.ClubProfileInsertPhoto(ctx, store.ClubProfileInsertPhotoParams{ClubID: p.ClubID,
				Path: fmt.Sprintf("%s/%d/%s.%s", photoDir, p.ClubID, names[i], ph.ext), SortOrder: order, Now: &now})
			if err != nil {
				return err
			}
			if err := q.ClubProfileInsertPhotoImage(ctx, store.ClubProfileInsertPhotoImageParams{PhotoID: id, ClubID: p.ClubID,
				ContentType: ph.contentType, Data: ph.data, Now: now}); err != nil {
				return err
			}
		}
		return logClub(ctx, q, now, "club.photos_added", nil, logPhotosAdded, map[string]string{"count": strconv.Itoa(len(photos))})
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.UploadClubPhotos200JSONResponse{Message: h.d.T(ctx, msgPhotosAdded, nil)}, nil
}

// DeleteClubPhoto — ClubProfileController::destroyPhoto(): фото своего клуба (иначе 404), журнал.
func (h *handlers) DeleteClubPhoto(ctx context.Context, req settingsapi.DeleteClubPhotoRequestObject) (settingsapi.DeleteClubPhotoResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		n, err := q.ClubProfileDeletePhoto(ctx, store.ClubProfileDeletePhotoParams{ClubID: p.ClubID, ID: req.Photo})
		if err != nil {
			return err
		}
		if n == 0 {
			return httpx.NotFound()
		}
		return logClub(ctx, q, now, "club.photo_deleted", nil, logPhotoDelete, nil)
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.DeleteClubPhoto200JSONResponse{Message: h.d.T(ctx, msgPhotoDeleted, nil)}, nil
}

// SetClubPhotoCover — ClubProfileController::coverPhoto(): фото становится первым; в журнал не пишется.
func (h *handlers) SetClubPhotoCover(ctx context.Context, req settingsapi.SetClubPhotoCoverRequestObject) (settingsapi.SetClubPhotoCoverResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		n, err := q.ClubProfileCoverPhoto(ctx, store.ClubProfileCoverPhotoParams{ClubID: p.ClubID, ID: req.Photo, Now: &now})
		if err == nil && n == 0 {
			return httpx.NotFound()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.SetClubPhotoCover200JSONResponse{Message: h.d.T(ctx, msgCoverChanged, nil)}, nil
}

// SaveClubZoneProfile — ClubProfileController::updateZone(): описание зоны и оборудование.
func (h *handlers) SaveClubZoneProfile(ctx context.Context, req settingsapi.SaveClubZoneProfileRequestObject) (settingsapi.SaveClubZoneProfileResponseObject, error) {
	var b settingsapi.ZoneProfileRequest
	if req.Body != nil {
		b = *req.Body
	}
	description := trimmed(b.Description)
	var fe httpx.FormErrors
	if utf8.RuneCountInString(description) > catalog.MaxZoneDescription {
		fe.Rule("description", "max_string", strconv.Itoa(catalog.MaxZoneDescription))
	}
	in := map[string]string{}
	for _, f := range catalog.DeviceFields() {
		if b.Devices == nil {
			break
		}
		v := trimmed((*b.Devices)[f.Key])
		if utf8.RuneCountInString(v) > catalog.MaxDevice {
			fe.Rule("devices."+f.Key, "max_string", strconv.Itoa(catalog.MaxDevice))
		}
		in[f.Key] = v
	}
	if err := fe.Err(); err != nil {
		return nil, err
	}
	var doc json.RawMessage
	if devices := catalog.CleanDevices(in); devices != nil {
		var err error
		if doc, err = json.Marshal(devices); err != nil {
			return nil, err
		}
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var name string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		zone, err := q.ClubProfileZoneForUpdate(ctx, store.ClubProfileZoneForUpdateParams{ClubID: p.ClubID, ID: req.Zone})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		name = zone.Name
		if err := q.ClubProfileSetZone(ctx, store.ClubProfileSetZoneParams{ClubID: p.ClubID, ID: zone.ID,
			Description: nullable(description), Devices: doc, Now: &now}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "club.zone_devices_changed", UserID: p.UserID, SubjectType: subjectZone, SubjectID: zone.ID, At: now,
		}.Translated(logZoneDevices, map[string]string{"zone": zone.Name}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.SaveClubZoneProfile200JSONResponse{Message: h.d.T(ctx, msgZoneSaved, map[string]string{"zone": name})}, nil
}

// clubPhoto — ClubProfileController::photo(): публичное фото клуба по случайному имени файла (гостевое приложение
// грузит его без токена). Строка ищется по полному пути «club-photos/<клуб>/<файл>»; иначе 404. Заголовки — как в
// старом.
func (h *handlers) clubPhoto(w http.ResponseWriter, r *http.Request) {
	clubID, err := strconv.ParseInt(chi.URLParam(r, "club"), 10, 64)
	file := chi.URLParam(r, "file")
	if err != nil || clubID <= 0 || !photoFileRe.MatchString(file) || h.d.PlatformPool == nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	var row store.ClubProfilePublicPhotoRow
	err = db.InPlatformTx(r.Context(), h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		row, err = q.ClubProfilePublicPhoto(r.Context(), store.ClubProfilePublicPhotoParams{ClubID: clubID,
			Path: fmt.Sprintf("%s/%d/%s", photoDir, clubID, file)})
		return err
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.d.Log().ErrorContext(r.Context(), "club photo", "club_id", clubID, "err", err)
		}
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", row.ContentType)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'")
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(row.Data))
}
