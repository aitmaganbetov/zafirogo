package catalog

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Profile — профиль клуба для приложения (clubs.profile). Пустые поля в JSON не пишутся.
type Profile struct {
	Description string `json:"description,omitempty"`
	Phone       string `json:"phone,omitempty"`
	Hours       string `json:"hours,omitempty"`
	Instagram   string `json:"instagram,omitempty"`
}

// Empty — профиль не заполнен ($profile ?: null — в базе NULL).
func (p Profile) Empty() bool { return p == Profile{} }

// Пределы длины полей. Laravel: ClubProfileController::update(), updateZone(); SettingsController::location().
const (
	MaxDescription     = 2000
	MaxPhone           = 32
	MaxHours           = 100
	MaxInstagram       = 64
	MaxAddress         = 255
	MaxZoneDescription = 500
	MaxDevice          = 120
)

// Тексты своих правил формы (ключи перевода).
const (
	MsgPhoneFormat     = "Телефон — только цифры, пробелы, скобки, «+» и «-»."
	MsgInstagramFormat = "Instagram — имя аккаунта, например @club."
)

// regex:/^[+\d\s()\-]+$/ и regex:/^@?[A-Za-z0-9._]+$/ (PCRE без u: \d и \s — ASCII; \s включает \v; «$» допускает
// перевод строки в конце).
var (
	phoneRe     = regexp.MustCompile(`^[+0-9\t\n\v\f\r ()\-]+\n?\z`)
	instagramRe = regexp.MustCompile(`^@?[A-Za-z0-9._]+\n?\z`)
)

// ProfileError — нарушенное правило поля профиля.
type ProfileError struct {
	Field string
	// Rule — "max" (Max — предел) или "regex" (Message — свой текст).
	Rule    string
	Max     int
	Message string
}

// Trim — TrimStrings Laravel: пробельные символы Unicode (\s с флагом u), BOM, ZWSP и LRM по краям.
// Laravel: Illuminate\Support\Str::trim() — '~^[\s\x{FEFF}\x{200B}\x{200E}]+|[\s\x{FEFF}\x{200B}\x{200E}]+$~u'.
func Trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\ufeff' || r == '\u200b' || r == '\u200e'
	})
}

// NewProfile проверяет поля формы «О клубе» (значения — после Trim) и собирает профиль: «@» у Instagram убирается.
// Ошибки — в порядке правил.
// Laravel: app/Http/Controllers/ClubProfileController.php ClubProfileController::update().
func NewProfile(description, phone, hours, instagram string) (Profile, []ProfileError) {
	var errs []ProfileError
	maxLen := func(field, v string, n int) {
		if utf8.RuneCountInString(v) > n {
			errs = append(errs, ProfileError{Field: field, Rule: "max", Max: n})
		}
	}
	maxLen("description", description, MaxDescription)
	maxLen("phone", phone, MaxPhone)
	if phone != "" && !phoneRe.MatchString(phone) {
		errs = append(errs, ProfileError{Field: "phone", Rule: "regex", Message: MsgPhoneFormat})
	}
	maxLen("hours", hours, MaxHours)
	maxLen("instagram", instagram, MaxInstagram)
	if instagram != "" && !instagramRe.MatchString(instagram) {
		errs = append(errs, ProfileError{Field: "instagram", Rule: "regex", Message: MsgInstagramFormat})
	}
	if len(errs) > 0 {
		return Profile{}, errs
	}
	return Profile{Description: description, Phone: phone, Hours: hours, Instagram: strings.TrimLeft(instagram, "@")}, nil
}
