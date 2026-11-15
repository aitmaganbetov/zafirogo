// Package settings — REST-раздел: настройки клуба (api/settings.yaml) — «Настройка клуба», «Языки», «Финансы»,
// «Гости», PIN выхода из агента, логотип и картинка экрана входа, город и адрес, «Профиль клуба» (описание, фото,
// оборудование зон — то, что видит гость в приложении); публичная отдача картинок агенту и приложению.
//
// Laravel: app/Http/Controllers/SettingsController.php, LanguageController.php, ClubLogoController.php,
// ClubSignInImageController.php, ClubProfileController.php, app/Http/Requests/SaveSettingsRequest.php. Конвенции — api/README.md.
package settings

import (
	"github.com/go-chi/chi/v5"

	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — операция → маршрут Laravel (RequireClubArea::ROUTES → право): просмотр — settings.view, запись —
// settings.update, PIN и время обслуживания — workers.update.
var access = httpx.Access{
	Routes: map[string]string{
		"GetClubSettings":    "settings.club",
		"GetClubLanguages":   "settings.languages",
		"SaveClubSettings":   "settings.save",
		"SaveClubTimezone":   "settings.timezone",
		"SaveClubLanguage":   "settings.language",
		"SaveAgentPin":       "settings.agent-pin",
		"SaveAgentStaffTime": "settings.agent-staff-time",
		"UploadClubLogo":     "settings.logo",
		"DeleteClubLogo":     "settings.logo.destroy",
		"UploadSigninImage":  "settings.signin-image",
		"DeleteSigninImage":  "settings.signin-image.destroy",
		// Тема В (docs/upstream-2026-11.md): город и адрес, профиль клуба для приложения.
		"SaveClubLocation":    "settings.location",
		"GetClubProfile":      "settings.profile",
		"SaveClubProfile":     "settings.profile.update",
		"UploadClubPhotos":    "settings.photos.store",
		"DeleteClubPhoto":     "settings.photos.destroy",
		"SetClubPhotoCover":   "settings.photos.cover",
		"SaveClubZoneProfile": "settings.profile.zone",
	},
}

type handlers struct {
	d *httpx.Deps
}

var _ settingsapi.StrictServerInterface = (*handlers)(nil)

// Section — настройки клуба. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "settings", Mount: mount, Limits: map[string]string{
		// routes/web.php: settings.photos.store — throttle:20,1.
		"POST /api/settings/profile/photos": "settings.photos.store",
	}}
}

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	si := settingsapi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[settingsapi.StrictMiddlewareFunc](access),
		settingsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	settingsapi.HandlerWithOptions(si, settingsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
	// ClubLogoController::show(), ClubSignInImageController::show(): публичные картинки по случайному имени файла
	// (агент грузит их без токена; новая загрузка — новый URL).
	r.Get("/club-logos/{club}/{file}", h.image(kindLogo))
	r.Get("/club-signin-images/{club}/{file}", h.image(kindSignin))
	// ClubProfileController::photo(): фото клуба для гостевого приложения — тоже публичные.
	r.Get("/club-photos/{club}/{file}", h.clubPhoto)
}
