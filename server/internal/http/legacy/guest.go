package legacy

// Маршруты mobileclient (routes/api.php, группа auth:sanctum + current-club + subscription): новости, журнал,
// QR-вход на ПК, друзья и перевод по коду из Telegram. Laravel: GuestNewsController, GuestHistoryController
// (App\Services\GuestHistory), GuestQrLoginController, GuestFriendController (App\Services\Friends,
// FriendTransferCodes).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/friends"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты (русский текст = ключ перевода).
const (
	msgFeatureOff       = "Эта возможность отключена в клубе."
	msgQRExpired        = "QR-код устарел. Обновите его на ПК и отсканируйте снова."
	msgQROtherClub      = "QR-код относится к другому клубу."
	msgQRApproved       = "Вход подтверждён. Через несколько секунд ПК откроется под вашей учётной записью."
	msgPhoneDigits      = "Введите 10 цифр номера после +7."
	msgFriendRequested  = "Заявка отправлена."
	msgFriendsNow       = "Вы теперь друзья."
	msgDone             = "Готово."
	msgTransferred      = "Переведено :amount."
	msgTransferCodeSent = "Код для подтверждения перевода отправлен в Telegram. Код действует :minutes мин."
	msgTransferCodesOff = "Подтверждение кодом из Telegram пока не подключено."
)

// FriendTransferCodes::CODE_MINUTES, RESEND_SECONDS; GuestNewsController::LIMIT; GuestHistory::PAGE.
const (
	transferCodeMinutes   = 5
	transferResendSeconds = 60
	newsLimit             = 10
	historyPage           = 30
)

// guestActor — кто действует (ledger, аудит): клиент из приложения.
func guestActor(r *http.Request) billing.BalanceActor {
	a := billing.BalanceActor{UserID: httpx.Principal(r.Context()).UserID, Kind: "guest", RequestID: middleware.GetReqID(r.Context())}
	if ip, err := netip.ParseAddr(httpx.HostOnly(r.RemoteAddr)); err == nil {
		a.IP = ip.Unmap()
	}
	return a
}

// settings — настройки клуба запроса.
func (a *API) settings(ctx context.Context) (clubSetting, error) {
	var cs clubSetting
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, httpx.ClubID(ctx))
		cs = parseClubSetting(raw)
		return err
	})
	return cs, err
}

// requireFeature — GuestFeatures::ensure(): 403 «Эта возможность отключена в клубе.»; false — ответ записан.
func (a *API) requireFeature(w http.ResponseWriter, r *http.Request, feature string) bool {
	cs, err := a.settings(r.Context())
	if err != nil {
		a.fail(w, r, "settings", err)
		return false
	}
	if !cs.features()[feature] {
		writeMessage(w, http.StatusForbidden, a.t(r, msgFeatureOff, nil))
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Новости (GuestNewsController)
// ---------------------------------------------------------------------------

type newsItem struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	ImageURL    *string `json:"image_url"`
	PublishedAt *string `json:"published_at"`
}

func (a *API) newsItem(id int64, title, text string, image *string, at *time.Time) newsItem {
	n := newsItem{ID: id, Title: title, Body: text, PublishedAt: iso(at)}
	if image != nil && *image != "" { // News::imageUrl(): route('news-images.show', [$news, basename($path)])
		u := strings.TrimRight(a.o.PublicBaseURL, "/") + "/news-images/" + strconv.FormatInt(id, 10) + "/" + basename(*image)
		n.ImageURL = &u
	}
	return n
}

func basename(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// news — GET /api/guest/news: {platform: [...], club: [...]} по 10.
func (a *API) news(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Platform []newsItem `json:"platform"`
		Club     []newsItem `json:"club"`
	}{[]newsItem{}, []newsItem{}}
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		platform, err := q.GuestListPlatformNews(ctx, newsLimit)
		if err != nil {
			return err
		}
		clubID := httpx.ClubID(ctx)
		own, err := q.GuestListClubNews(ctx, store.GuestListClubNewsParams{ClubID: &clubID, PageSize: newsLimit})
		if err != nil {
			return err
		}
		for _, n := range platform {
			out.Platform = append(out.Platform, a.newsItem(n.ID, n.Title, n.Body, n.ImagePath, n.CreatedAt))
		}
		for _, n := range own {
			out.Club = append(out.Club, a.newsItem(n.ID, n.Title, n.Body, n.ImagePath, n.CreatedAt))
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, "news", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Журнал клиента (GuestHistory::page)
// ---------------------------------------------------------------------------

var (
	historyOwn      = []string{"tariff.purchased", "time.purchased", "shop.purchased", "time.refunded", "balance.transferred", "session.transferred", "auth.api_login", "auth.qr_login"}
	historyReceived = []string{"user.balance_topped_up", "user.balance_refunded", "user.balance_written_off", "balance.transferred", "shop.order_cancelled", "time.refunded"} //nolint:misspell // код события старого журнала
	historyFilters  = map[string][2][]string{
		"purchases": {{"tariff.purchased", "time.purchased", "shop.purchased", "time.refunded"}, {"shop.order_cancelled", "time.refunded"}}, //nolint:misspell // код события старого журнала
		"top_ups":   {{}, {"user.balance_topped_up", "user.balance_refunded", "user.balance_written_off"}},
		"transfers": {{"balance.transferred"}, {"balance.transferred"}},
		"visits":    {{"session.transferred", "auth.api_login", "auth.qr_login"}, {}},
	}
)

type historyLog struct {
	ID        int64
	UserID    int64
	Event     string
	SubjectID *int64
	Details   map[string]any
	CreatedAt time.Time
}

type historyEntry struct {
	ID          int64   `json:"id"`
	At          *string `json:"at"`
	Kind        string  `json:"kind"`
	Title       string  `json:"title"`
	Note        *string `json:"note"`
	Amount      *string `json:"amount"`
	BonusAmount *string `json:"bonus_amount"`
}

// history — GET /api/guest/history?before=&filter=: {data: [...], next_before}.
func (a *API) history(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.Principal(ctx)
	qv := r.URL.Query()
	before := int64(1<<63 - 1)
	if s := qv.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			a.writeError(w, r, httpx.RuleError("before", "integer"))
			return
		}
		if n < 1 {
			a.writeError(w, r, httpx.RuleErrorWith("before", "min_numeric", "1"))
			return
		}
		before = n
	}
	own, received := historyOwn, historyReceived
	if f := qv.Get("filter"); f != "" {
		sel, ok := historyFilters[f]
		if !ok {
			a.writeError(w, r, httpx.RuleError("filter", "in"))
			return
		}
		own, received = sel[0], sel[1]
	}
	out := struct {
		Data       []historyEntry `json:"data"`
		NextBefore *int64         `json:"next_before"`
	}{Data: []historyEntry{}}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var logs []historyLog
		seen := map[int64]bool{}
		add := func(l historyLog) {
			if !seen[l.ID] {
				seen[l.ID] = true
				logs = append(logs, l)
			}
		}
		if len(own) > 0 {
			rows, err := q.GuestHistoryOwn(ctx, store.GuestHistoryOwnParams{ClubID: p.ClubID, UserID: p.UserID, Events: own,
				BeforeID: before, PageSize: historyPage + 1})
			if err != nil {
				return err
			}
			for _, r := range rows {
				add(historyLog{ID: r.ID, UserID: r.UserID, Event: r.Event, SubjectID: r.SubjectID, Details: details(r.Details), CreatedAt: r.CreatedAt})
			}
		}
		if len(received) > 0 {
			st, uid := morphUser, p.UserID
			rows, err := q.GuestHistoryReceived(ctx, store.GuestHistoryReceivedParams{ClubID: p.ClubID, SubjectType: &st, UserID: &uid,
				Events: received, BeforeID: before, PageSize: historyPage + 1})
			if err != nil {
				return err
			}
			for _, r := range rows {
				add(historyLog{ID: r.ID, UserID: r.UserID, Event: r.Event, SubjectID: r.SubjectID, Details: details(r.Details), CreatedAt: r.CreatedAt})
			}
		}
		slices.SortFunc(logs, func(x, y historyLog) int { return int(y.ID - x.ID) })
		page := logs[:min(len(logs), historyPage)]
		if len(logs) > historyPage {
			last := page[len(page)-1].ID
			out.NextBefore = &last
		}
		tariffs, users, err := historyNames(ctx, q, p.ClubID, page)
		if err != nil {
			return err
		}
		for _, l := range page {
			out.Data = append(out.Data, a.historyEntry(r, l, p.UserID, tariffs, users))
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, "history", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func details(raw []byte) map[string]any {
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	return d
}

// historyNames — GuestHistory::names(): тарифы покупок и друзья переводов страницы.
func historyNames(ctx context.Context, q *store.Queries, clubID int64, page []historyLog) (map[int64]string, map[int64]string, error) {
	var tariffIDs, userIDs []int64
	for _, l := range page {
		switch l.Event {
		case "tariff.purchased":
			if l.SubjectID != nil {
				tariffIDs = append(tariffIDs, *l.SubjectID)
			}
		case "balance.transferred":
			userIDs = append(userIDs, l.UserID)
			if to := int64Of(l.Details["to_user_id"]); to > 0 {
				userIDs = append(userIDs, to)
			}
		}
	}
	tariffs, users := map[int64]string{}, map[int64]string{}
	if len(tariffIDs) > 0 {
		rows, err := q.GuestTariffNames(ctx, store.GuestTariffNamesParams{ClubID: clubID, Ids: tariffIDs})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			tariffs[r.ID] = r.Name
		}
	}
	if len(userIDs) > 0 {
		rows, err := q.GuestUserNames(ctx, store.GuestUserNamesParams{ClubID: clubID, Ids: userIDs})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			users[r.ID] = r.Name
		}
	}
	return tariffs, users, nil
}

func int64Of(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// historyAmount — is_numeric($x) && (float) $x > 0 → number_format((float) $x, 2, '.', ”) со знаком; иначе null.
func historyAmount(v any, negative bool) *string {
	var s string
	switch x := v.(type) {
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s = strings.TrimSpace(x)
	default:
		return nil
	}
	if !money.PHPIsNumeric(s) {
		return nil
	}
	t, err := money.ParseTengeRounded(s)
	if err != nil || t <= 0 {
		return nil
	}
	out := moneyStr(int64(t))
	if negative {
		out = "-" + out
	}
	return &out
}

func first(d map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := d[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func strOf(v any) string { s, _ := v.(string); return s }

func nameOr(names map[int64]string, id int64) string {
	if n := names[id]; n != "" {
		return n
	}
	return "—"
}

func ptr[T any](v T) *T { return &v }

// historyEntry — GuestHistory::entry() (тексты переведены на язык запроса).
func (a *API) historyEntry(r *http.Request, l historyLog, userID int64, tariffs, users map[int64]string) historyEntry {
	d := l.Details
	e := historyEntry{ID: l.ID, At: iso(&l.CreatedAt), Kind: l.Event}
	t := func(key string, params map[string]string) string { return a.t(r, key, params) }
	switch l.Event {
	case "tariff.purchased":
		e.Kind = "purchase"
		name := "—"
		if l.SubjectID != nil && tariffs[*l.SubjectID] != "" {
			name = tariffs[*l.SubjectID]
		}
		e.Title = t("Покупка тарифа «:tariff»", map[string]string{"tariff": name})
		if v, ok := d["duration_minutes"]; ok && v != nil {
			e.Note = ptr(t(":count минута|:count минуты|:count минут", map[string]string{"count": strconv.FormatInt(int64Of(v), 10)}))
		}
		e.Amount = historyAmount(first(d, "main_balance_amount", "amount"), true)
		e.BonusAmount = historyAmount(d["bonus_balance_amount"], true)
	case "time.purchased":
		e.Kind = "purchase"
		e.Title = t("Поминутная оплата: :count мин.", map[string]string{"count": strconv.FormatInt(int64Of(d["duration_minutes"]), 10)})
		if v, ok := d["price_per_hour"]; ok && v != nil {
			e.Note = ptr(t(":price за час", map[string]string{"price": scalar(v)}))
		}
		e.Amount = historyAmount(first(d, "main_balance_amount", "amount"), true)
		e.BonusAmount = historyAmount(d["bonus_balance_amount"], true)
	case "shop.purchased":
		e.Kind = "shop"
		e.Title = t("Заказ в магазине клуба", nil)
		if items, ok := d["items"].([]any); ok {
			var parts []string
			for _, it := range items {
				line, ok := it.(map[string]any)
				if !ok {
					continue
				}
				name := "—"
				if v, ok := line["name"]; ok && v != nil {
					name = scalar(v)
				}
				parts = append(parts, name+" × "+strconv.FormatInt(int64Of(line["quantity"]), 10))
			}
			if len(parts) > 0 {
				e.Note = ptr(strings.Join(parts, ", "))
			}
		}
		e.Amount = historyAmount(d["amount"], true)
	case "shop.order_cancelled": //nolint:misspell // код события старого журнала
		e.Kind = "refund"
		e.Title = t("Возврат за отменённый заказ", nil)
		e.Amount = historyAmount(d["amount"], false)
	case "time.refunded":
		e.Kind = "refund"
		e.Title = t("Возврат за неиспользованное время", nil)
		e.Amount = historyAmount(d["main_balance_amount"], false)
		e.BonusAmount = historyAmount(d["bonus_balance_amount"], false)
	case "user.balance_topped_up":
		e.Kind = "top_up"
		e.Title = t("Пополнение на кассе", nil)
		if v, ok := d["payment_method"].(string); ok {
			e.Note = &v
		}
		// in_array(balance_type, [__('бонусный баланс') на ru/kk/en]).
		bt := strOf(d["balance_type"])
		bonusType := bt != "" && slices.ContainsFunc([]string{"ru", "kk", "en"}, func(l string) bool {
			return a.tl(r.Context(), l, "бонусный баланс", nil) == bt
		})
		if bonusType {
			e.BonusAmount = historyAmount(d["amount"], false)
		} else {
			e.Amount = historyAmount(d["amount"], false)
			if v, ok := d["bonus_amount"]; ok && v != nil {
				e.BonusAmount = historyAmount(v, false)
			}
		}
	case "user.balance_refunded":
		e.Kind = "top_up"
		e.Title = t("Возврат пополнения на кассе", nil)
		if f := strOf(d["balance_field"]); f == "bonus_balance" {
			e.BonusAmount = historyAmount(d["amount"], true)
		} else {
			e.Amount = historyAmount(d["amount"], true)
			e.BonusAmount = historyAmount(d["bonus_taken_back"], true)
		}
	case "user.balance_written_off":
		e.Kind = "top_up"
		e.Title = t("Списание с баланса администрацией клуба", nil)
		if strOf(d["balance_field"]) == "bonus_balance" {
			e.BonusAmount = historyAmount(d["amount"], true)
		} else {
			e.Amount = historyAmount(d["amount"], true)
		}
	case "balance.transferred":
		if l.UserID == userID {
			e.Kind = "transfer_out"
			e.Title = t("Перевод другу :name", map[string]string{"name": nameOr(users, int64Of(d["to_user_id"]))})
			e.Amount = historyAmount(d["amount"], true)
		} else {
			e.Kind = "transfer_in"
			e.Title = t("Перевод от друга :name", map[string]string{"name": nameOr(users, l.UserID)})
			e.Amount = historyAmount(d["amount"], false)
		}
	case "session.transferred":
		e.Kind = "seat"
		to := "—"
		if v, ok := d["to"]; ok && v != nil {
			to = scalar(v)
		}
		e.Title = t("Пересадка на ПК :computer", map[string]string{"computer": to})
		if v, ok := d["from"]; ok && v != nil {
			e.Note = ptr(t("С ПК :computer", map[string]string{"computer": scalar(v)}))
		}
	default:
		e.Kind = "login"
		if l.Event == "auth.qr_login" {
			e.Title = t("Вход на ПК по QR-коду", nil)
		} else {
			e.Title = t("Вход в аккаунт", nil)
		}
		if v := first(d, "computer", "device"); v != nil {
			e.Note = ptr(scalar(v))
		}
	}
	return e
}

// scalar — (string) скаляра PHP для подстановки.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------------------
// QR-вход на ПК (GuestQrLoginController)
// ---------------------------------------------------------------------------

func qrHash(code string) string {
	s := sha256.Sum256([]byte(code))
	return hex.EncodeToString(s[:])
}

// openQr — QrLogins::findOpen() и отказ rejection(): 410 qr_expired / 403 other_club. nil — ответ записан.
func (a *API) openQr(w http.ResponseWriter, r *http.Request, code string) *store.PcLoginFindOpenRow {
	var row *store.PcLoginFindOpenRow
	if a.d.PlatformPool == nil {
		a.fail(w, r, "qr", httpx.ErrNotConfigured)
		return nil
	}
	err := db.InPlatformTx(r.Context(), a.d.PlatformPool, func(q *store.Queries) error {
		x, err := q.PcLoginFindOpen(r.Context(), store.PcLoginFindOpenParams{CodeHash: qrHash(code), Now: a.d.NowFunc()})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		row = &x
		return err
	})
	switch {
	case err != nil:
		a.fail(w, r, "qr", err)
		return nil
	case row == nil:
		writeCode(w, http.StatusGone, a.t(r, msgQRExpired, nil), "qr_expired")
		return nil
	case row.ClubID != httpx.ClubID(r.Context()):
		writeCode(w, http.StatusForbidden, a.t(r, msgQROtherClub, nil), "other_club")
		return nil
	}
	return row
}

func qrComputer(row *store.PcLoginFindOpenRow) *string {
	if row.Number == nil {
		return nil
	}
	return ptr(club.ComputerName(row.DisplayName, row.DeviceName, *row.Number))
}

// qrShow — GET /api/guest/qr-login/{code}: {computer, club_name}.
func (a *API) qrShow(w http.ResponseWriter, r *http.Request) {
	row := a.openQr(w, r, chi.URLParam(r, "code"))
	if row == nil {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Computer *string `json:"computer"`
		ClubName string  `json:"club_name"`
	}{qrComputer(row), row.ClubName})
}

// qrApprove — POST /api/guest/qr-login/{code}: QrLogins::approve() (guestapi.Server.ApproveQrLogin: ПК получит
// QrLoginApproved). Отказ подтверждения — 422 по полю code с текстом старого кода.
func (a *API) qrApprove(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	row := a.openQr(w, r, code)
	if row == nil {
		return
	}
	if a.d.ApproveQrLogin == nil {
		a.fail(w, r, "qr approve", httpx.ErrNotConfigured)
		return
	}
	p := httpx.Principal(r.Context())
	if err := a.d.ApproveQrLogin(r.Context(), row.ClubID, code, p.UserID); err != nil {
		st, ok := status.FromError(err)
		switch {
		case ok && st.Code() == codes.NotFound:
			writeCode(w, http.StatusGone, a.t(r, msgQRExpired, nil), "qr_expired")
		case ok && (st.Code() == codes.InvalidArgument || st.Code() == codes.FailedPrecondition || st.Code() == codes.PermissionDenied):
			writeValidation(w, "code", a.t(r, st.Message(), nil))
		default:
			a.fail(w, r, "qr approve", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Message  string  `json:"message"`
		Computer *string `json:"computer"`
	}{a.t(r, msgQRApproved, nil), qrComputer(row)})
}

// ---------------------------------------------------------------------------
// Друзья (GuestFriendController, App\Services\Friends)
// ---------------------------------------------------------------------------

type friendJSON struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Phone *string `json:"phone"`
}

func friendList(in []friends.Friend) []friendJSON {
	out := make([]friendJSON, 0, len(in))
	for _, f := range in {
		var phone *string
		if f.MaskedPhone != "" {
			phone = ptr(f.MaskedPhone)
		}
		out = append(out, friendJSON{ID: f.FriendshipID, Name: f.Name, Phone: phone})
	}
	return out
}

// friendsBody — ['message'?, 'features'?, ...$this->friends->overview()].
type friendsBody struct {
	Message  *string         `json:"message,omitempty"`
	Features map[string]bool `json:"features,omitempty"`
	Friends  []friendJSON    `json:"friends"`
	Incoming []friendJSON    `json:"incoming"`
	Outgoing []friendJSON    `json:"outgoing"`
}

func overviewBody(o friends.Overview) friendsBody {
	return friendsBody{Friends: friendList(o.Friends), Incoming: friendList(o.Incoming), Outgoing: friendList(o.Outgoing)}
}

func (a *API) rec(ctx context.Context, q *store.Queries, e activity.Entry) error {
	_, err := activity.Record(ctx, q, e)
	return err
}

// friendsError — ошибки friends в формате Laravel: правило — 422, нет дружбы в клубе — 404 модели.
func (a *API) friendsError(w http.ResponseWriter, r *http.Request, id string, err error) {
	var fe *friends.Error
	switch {
	case errors.Is(err, friends.ErrNotFound):
		modelNotFound(w, "Friendship", id)
	case errors.As(err, &fe):
		writeValidation(w, fe.Field, a.t(r, fe.Message, nil))
	default:
		a.fail(w, r, "friends", err)
	}
}

// friendsIndex — GET /api/guest/friends: {features, friends, incoming, outgoing}.
func (a *API) friendsIndex(w http.ResponseWriter, r *http.Request) {
	p := httpx.Principal(r.Context())
	var out friendsBody
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, p.ClubID)
		if err != nil {
			return err
		}
		o, err := friends.List(ctx, q, p.ClubID, p.UserID)
		out = overviewBody(o)
		out.Features = parseClubSetting(raw).features()
		return err
	})
	if err != nil {
		a.fail(w, r, "friends", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// friendsStore — POST /api/guest/friends {phone}: 201 {message, friends, incoming, outgoing}.
func (a *API) friendsStore(w http.ResponseWriter, r *http.Request) {
	if !a.requireFeature(w, r, "friends") {
		return
	}
	b := readBody(r)
	raw, _ := b.str("phone")
	digits := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, raw)
	if len(digits) == 11 && (digits[0] == '7' || digits[0] == '8') {
		digits = digits[1:]
	}
	phone := "+7" + digits
	if !kzPhoneRe.MatchString(phone) {
		writeValidation(w, "phone", a.t(r, msgPhoneDigits, nil))
		return
	}
	p := httpx.Principal(r.Context())
	var out friendsBody
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		accepted, err := friends.Request(ctx, q, a.rec, p.ClubID, p.UserID, phone, a.d.NowFunc())
		if err != nil {
			return err
		}
		o, err := friends.List(ctx, q, p.ClubID, p.UserID)
		out = overviewBody(o)
		msg := msgFriendRequested
		if accepted {
			msg = msgFriendsNow
		}
		out.Message = ptr(a.t(r, msg, nil))
		return err
	})
	if err != nil {
		a.friendsError(w, r, "", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// friendshipID — {friendship} пути: не число — 404 модели (route model binding).
func friendshipID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := chi.URLParam(r, "friendship")
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		modelNotFound(w, "Friendship", s)
		return 0, false
	}
	return id, true
}

// friendsAccept — POST /api/guest/friends/{friendship}/accept.
func (a *API) friendsAccept(w http.ResponseWriter, r *http.Request) {
	id, ok := friendshipID(w, r)
	if !ok || !a.requireFeature(w, r, "friends") {
		return
	}
	a.friendAction(w, r, id, msgFriendsNow, func(ctx context.Context, q *store.Queries, clubID, userID int64) error {
		return friends.Accept(ctx, q, a.rec, clubID, userID, id, a.d.NowFunc())
	})
}

// friendsRemove — POST /api/guest/friends/{friendship}/remove (без проверки функции, как в старом).
func (a *API) friendsRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := friendshipID(w, r)
	if !ok {
		return
	}
	a.friendAction(w, r, id, msgDone, func(ctx context.Context, q *store.Queries, clubID, userID int64) error {
		return friends.Remove(ctx, q, a.rec, clubID, userID, id)
	})
}

func (a *API) friendAction(w http.ResponseWriter, r *http.Request, id int64, msg string, fn func(ctx context.Context, q *store.Queries, clubID, userID int64) error) {
	p := httpx.Principal(r.Context())
	var out friendsBody
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		if err := fn(ctx, q, p.ClubID, p.UserID); err != nil {
			return err
		}
		o, err := friends.List(ctx, q, p.ClubID, p.UserID)
		out = overviewBody(o)
		out.Message = ptr(a.t(r, msg, nil))
		return err
	})
	if err != nil {
		a.friendsError(w, r, strconv.FormatInt(id, 10), err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// transferAmount — правило 'amount' => ['required', 'string', 'max:20']; ok=false — ответ записан.
func (a *API) transferAmount(w http.ResponseWriter, r *http.Request, b body) (string, bool) {
	amount, _ := b.str("amount")
	switch {
	case !b.filled("amount"):
		a.writeError(w, r, httpx.RuleError("amount", "required"))
		return "", false
	case !b.isString("amount"):
		a.writeError(w, r, httpx.RuleError("amount", "string"))
		return "", false
	case len([]rune(amount)) > 20:
		a.writeError(w, r, httpx.RuleErrorWith("amount", "max_string", "20"))
		return "", false
	}
	return amount, true
}

// friendshipExists — route model binding {friendship} в клубе токена.
func (a *API) friendshipExists(ctx context.Context, id int64) (bool, error) {
	found := false
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		_, err := q.GuestLockFriendship(ctx, store.GuestLockFriendshipParams{ClubID: httpx.ClubID(ctx), ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	return found, err
}

// transferCode — POST /api/guest/friends/{friendship}/transfer/code {amount}: код в Telegram (FriendTransferCodes,
// сервис Telegram — Options.TransferCodes).
func (a *API) transferCode(w http.ResponseWriter, r *http.Request) {
	id, ok := friendshipID(w, r)
	if !ok {
		return
	}
	if found, err := a.friendshipExists(r.Context(), id); err != nil || !found {
		if err != nil {
			a.fail(w, r, "friendship", err)
		} else {
			modelNotFound(w, "Friendship", strconv.FormatInt(id, 10))
		}
		return
	}
	if !a.requireFeature(w, r, "balance_transfer") {
		return
	}
	amount, ok := a.transferAmount(w, r, readBody(r))
	if !ok {
		return
	}
	if a.o.TransferCodes == nil {
		writeValidation(w, "code", a.t(r, msgTransferCodesOff, nil))
		return
	}
	p := httpx.Principal(r.Context())
	if err := a.o.TransferCodes.Send(r.Context(), p.ClubID, p.UserID, id, money.ParseTransferAmount(amount)); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Message       string `json:"message"`
		CodeMinutes   int    `json:"code_minutes"`
		ResendSeconds int    `json:"resend_seconds"`
	}{a.t(r, msgTransferCodeSent, map[string]string{"minutes": strconv.Itoa(transferCodeMinutes)}), transferCodeMinutes, transferResendSeconds})
}

// transferConfirm — POST /api/guest/friends/{friendship}/transfer/confirm {amount, code}: перевод по коду
// (billing.ConfirmFriendTransferCode — через ledger; неверный код засчитывает попытку отдельной транзакцией).
func (a *API) transferConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := friendshipID(w, r)
	if !ok {
		return
	}
	if found, err := a.friendshipExists(r.Context(), id); err != nil || !found {
		if err != nil {
			a.fail(w, r, "friendship", err)
		} else {
			modelNotFound(w, "Friendship", strconv.FormatInt(id, 10))
		}
		return
	}
	if !a.requireFeature(w, r, "balance_transfer") {
		return
	}
	b := readBody(r)
	amount, ok := a.transferAmount(w, r, b)
	if !ok {
		return
	}
	code, _ := b.str("code")
	switch {
	case !b.filled("code"):
		writeValidation(w, "code", a.t(r, msgCodeRequired, nil))
		return
	case !b.isString("code") || !codeRe.MatchString(code):
		writeValidation(w, "code", a.t(r, msgCodeFormat, nil))
		return
	}
	ctx := r.Context()
	p := httpx.Principal(ctx)
	var res billing.FriendTransferResult
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		res, err = billing.ConfirmFriendTransferCode(ctx, q, billing.ConfirmTransferCodeParams{
			ClubID: p.ClubID, UserID: p.UserID, FriendshipID: id, AmountInput: amount, Code: code, Now: a.d.NowFunc(),
			VerifyCode: func(hash, code string) bool { _, err := a.o.Passwords.Verify(ctx, code, hash); return err == nil },
			Actor:      guestActor(r),
		})
		if err != nil || res.Ledger.Replayed {
			return err
		}
		if err := billingsvc.RecordBalanceTransferred(ctx, q, p.ClubID, p.UserID, id, res); err != nil {
			return err
		}
		// BalanceChanged на ПК, где вошли отправитель и друг (как перевод агента, guestapi.transferResponse).
		var n billingsvc.SessionNotifier
		if a.d.Cash != nil && a.d.Cash.Notifier != nil {
			n = a.d.Cash.Notifier
		}
		return billingsvc.NotifyBalancesAfterCommit(ctx, q, n, p.ClubID, p.UserID, res.FriendID)
	})
	var mismatch *billing.TransferCodeMismatchError
	if errors.As(err, &mismatch) {
		if e := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
			return billing.RecordTransferCodeAttempt(ctx, q, p.ClubID, mismatch.CodeID)
		}); e != nil {
			a.o.Log.WarnContext(ctx, "transfer code attempt not recorded", "err", e)
		}
		a.writeError(w, r, mismatch.AsBalanceError())
		return
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// Friends::transfer(): ['balance' => formatCents(...), 'amount' => formatCents(...)] — строки "1500.00".
	writeJSON(w, http.StatusOK, struct {
		Message string `json:"message"`
		Balance string `json:"balance"`
	}{a.t(r, msgTransferred, map[string]string{"amount": moneyStr(int64(res.Amount))}), moneyStr(int64(res.Balance))})
}
