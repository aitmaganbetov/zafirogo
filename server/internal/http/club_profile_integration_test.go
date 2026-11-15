//go:build integration

package httpserver

// Тема В (docs/upstream-2026-11.md): город и адрес клуба, профиль клуба для гостевого приложения, фото, оборудование
// зон — HTTP → Postgres (RLS). Сценарии tests/Feature/ClubCatalogTest.php старого проекта: GS-566, GS-570.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// uploadPhotos — форма multipart с файлами photos[] (как старая форма settings.photos.store).
func (e *adminEnv) uploadPhotos(tok string, files ...[]byte) resp {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i, f := range files {
		fw, _ := w.CreateFormFile("photos[]", fmt.Sprintf("photo%d.png", i))
		_, _ = fw.Write(f)
	}
	_ = w.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/api/settings/profile/photos", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := resp{status: res.StatusCode, header: res.Header}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out.body)
	return out
}

// webpImage — заголовок WebP (RIFF/WEBP, блок VP8X) с размерами w×h: getimagesize() PHP и сервер читают только его.
func webpImage(w, h int) []byte {
	b := make([]byte, 30)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 22)
	copy(b[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(b[16:], 10)
	b[24], b[25], b[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	b[27], b[28], b[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	return b
}

// noisePNG — PNG w×h со случайными пикселями (почти не сжимается): файл нужного размера.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(r.UintN(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *adminEnv) text(sql string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.db.Owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatal(err)
	}
	if v == nil {
		return "<null>"
	}
	return *v
}

func TestClubLocation(t *testing.T) {
	e, _ := newG5Env(t, time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC))
	_, owner := e.staff("Владелец", "owner")
	_, cashier := e.staff("Кассир", "cashier")

	t.Run("GS-566 the club owner sets the city and address of the club", func(t *testing.T) {
		r := e.get(owner, "/api/settings")
		expect(t, r, http.StatusOK, "settings")
		cities, _ := r.body["cities"].([]any)
		if len(cities) != 25 || obj(cities[0])["key"] != "aktau" || obj(cities[0])["name"] != "Актау" || obj(cities[24])["key"] != "ekibastuz" {
			t.Fatalf("cities: %v", cities)
		}
		if r.body["city"] != nil || r.body["address"] != nil {
			t.Fatalf("location before: %v %v", r.body["city"], r.body["address"])
		}

		expectField(t, e.send("PUT", owner, "/api/settings/location", map[string]any{"city": "atlantis"}), "city", "The selected city is invalid.")
		expectField(t, e.send("PUT", owner, "/api/settings/location", map[string]any{"city": "aktobe", "address": strings.Repeat("я", 256)}),
			"address", "The address field must not be greater than 255 characters.")
		if e.text("SELECT city FROM clubs WHERE id = $1", e.club) != "<null>" {
			t.Fatal("city saved despite errors")
		}

		r = e.send("PUT", owner, "/api/settings/location", map[string]any{"city": "aktobe", "address": " пр. Абилкайыр хана, 1 "})
		expect(t, r, http.StatusOK, "location")
		if r.body["message"] != "Город и адрес сохранены." {
			t.Fatalf("message: %v", r.body)
		}
		if e.text("SELECT city FROM clubs WHERE id = $1", e.club) != "aktobe" ||
			e.text("SELECT address FROM clubs WHERE id = $1", e.club) != "пр. Абилкайыр хана, 1" {
			t.Fatal("location not saved")
		}
		if n := e.activityCount("club.location_changed", 0); n != 1 {
			t.Fatalf("location log: %d", n)
		}
		if d := e.text("SELECT description FROM activity_logs WHERE club_id = $1 AND event = 'club.location_changed'", e.club); d != "Город и адрес клуба изменены: Актобе, пр. Абилкайыр хана, 1" {
			t.Fatalf("log text: %q", d)
		}
		r = e.get(owner, "/api/settings")
		if r.body["city"] != "aktobe" || r.body["address"] != "пр. Абилкайыр хана, 1" {
			t.Fatalf("location after: %v", r.body)
		}

		// Без изменений — в журнал не пишется ($club->isDirty(['city', 'address'])).
		expect(t, e.send("PUT", owner, "/api/settings/location", map[string]any{"city": "aktobe", "address": "пр. Абилкайыр хана, 1"}), http.StatusOK, "same")
		if n := e.activityCount("club.location_changed", 0); n != 1 {
			t.Fatalf("log after the same location: %d", n)
		}

		// «Не указан»: клуба нет в каталоге; в журнале — «—».
		expect(t, e.send("PUT", owner, "/api/settings/location", map[string]any{"city": "", "address": nil}), http.StatusOK, "clear")
		if e.text("SELECT city FROM clubs WHERE id = $1", e.club) != "<null>" || e.text("SELECT address FROM clubs WHERE id = $1", e.club) != "<null>" {
			t.Fatal("location not cleared")
		}
		if e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'club.location_changed' AND description = 'Город и адрес клуба изменены: —'", e.club) != 1 {
			t.Fatal("clear log")
		}

		// Кассир — без права settings.update (и settings.view).
		expect(t, e.send("PUT", cashier, "/api/settings/location", map[string]any{"city": "almaty"}), http.StatusForbidden, "cashier")
		expect(t, e.get(cashier, "/api/settings/profile"), http.StatusForbidden, "cashier profile")
	})
}

func TestClubProfile(t *testing.T) {
	e, _ := newG5Env(t, time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC))
	_, owner := e.staff("Владелец", "owner")
	_, manager := e.staff("Менеджер", "manager")
	_, cashier := e.staff("Кассир", "cashier")
	zone := e.scalar("INSERT INTO club_zones (club_id, name, price_per_hour, sort_order, created_at, updated_at) VALUES ($1, 'VIP', 80000, 1, now(), now()) RETURNING id", e.club)
	e.exec(`INSERT INTO club_computers (club_id, club_zone_id, number, cpu_model, gpu_model, ram_total_mb, created_at, updated_at)
		VALUES ($1, $2, 1, '11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz', 'NVIDIA GeForce RTX 3060', 16253, now(), now())`, e.club, zone)
	empty := e.scalar("INSERT INTO club_zones (club_id, name, sort_order, created_at, updated_at) VALUES ($1, 'Пустая', 2, now(), now()) RETURNING id", e.club)

	// Чужой клуб: его зона и фото недоступны.
	other := e.db.Club(t, fmt.Sprintf("g5other%d", adminSeq.Add(1)))
	otherZone := e.scalar("INSERT INTO club_zones (club_id, name, created_at, updated_at) VALUES ($1, 'Чужая', now(), now()) RETURNING id", other)
	otherPhoto := e.scalar("INSERT INTO club_photos (club_id, path, sort_order, created_at, updated_at) VALUES ($1, $2, 1, now(), now()) RETURNING id",
		other, fmt.Sprintf("club-photos/%d/%s.png", other, strings.Repeat("c", 40)))

	t.Run("GS-570 page: zones with hardware hints from the agents", func(t *testing.T) {
		r := e.get(manager, "/api/settings/profile")
		expect(t, r, http.StatusOK, "profile")
		zones, _ := r.body["zones"].([]any)
		fields, _ := r.body["device_fields"].([]any)
		if len(zones) != 2 || len(fields) != 8 || r.body["photo_limit"] != float64(10) || len(r.body["photos"].([]any)) != 0 {
			t.Fatalf("page: %v", r.body)
		}
		z := obj(zones[0])
		det := obj(z["detected"])
		if z["name"] != "VIP" || z["computers_count"] != float64(1) || z["price_per_hour"] != float64(80000) ||
			det["cpu"] != "Intel Core i5-11400F" || det["gpu"] != "NVIDIA GeForce RTX 3060" || det["ram"] != "16 ГБ" || len(obj(z["devices"])) != 0 {
			t.Fatalf("zone: %v", z)
		}
		if z2 := obj(zones[1]); z2["computers_count"] != float64(0) || z2["price_per_hour"] != nil || len(obj(z2["detected"])) != 0 {
			t.Fatalf("empty zone: %v", z2)
		}
		if f := obj(fields[0]); f["key"] != "cpu" || f["label"] != "Процессор" {
			t.Fatalf("fields: %v", fields)
		}
		if f := obj(fields[7]); f["key"] != "chair" || f["label"] != "Кресло" {
			t.Fatalf("fields: %v", fields)
		}
	})

	t.Run("GS-570 description and contacts", func(t *testing.T) {
		r := e.send("PUT", owner, "/api/settings/profile", map[string]any{"description": "Лучший клуб", "phone": "+7 700 000 00 00",
			"hours": "Круглосуточно", "instagram": "@asia.game"})
		expect(t, r, http.StatusOK, "profile save")
		if r.body["message"] != "Описание клуба сохранено." {
			t.Fatalf("message: %v", r.body)
		}
		var saved map[string]string
		if err := json.Unmarshal([]byte(e.text("SELECT profile::text FROM clubs WHERE id = $1", e.club)), &saved); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"description": "Лучший клуб", "phone": "+7 700 000 00 00", "hours": "Круглосуточно", "instagram": "asia.game"}
		if fmt.Sprint(saved) != fmt.Sprint(want) {
			t.Fatalf("profile: %v", saved)
		}
		expectField(t, e.send("PUT", owner, "/api/settings/profile", map[string]any{"phone": "<script>"}), "phone",
			"Телефон — только цифры, пробелы, скобки, «+» и «-».")
		expectField(t, e.send("PUT", owner, "/api/settings/profile", map[string]any{"instagram": "клуб"}), "instagram",
			"Instagram — имя аккаунта, например @club.")
		expectField(t, e.send("PUT", owner, "/api/settings/profile", map[string]any{"description": strings.Repeat("я", 2001)}), "description",
			"The description field must not be greater than 2000 characters.")
		if e.text("SELECT profile ->> 'phone' FROM clubs WHERE id = $1", e.club) != "+7 700 000 00 00" {
			t.Fatal("profile changed by an invalid request")
		}
		p := obj(e.get(owner, "/api/settings/profile").body["profile"])
		if p["instagram"] != "asia.game" || p["hours"] != "Круглосуточно" {
			t.Fatalf("profile view: %v", p)
		}
		if n := e.activityCount("club.profile_changed", 0); n != 1 {
			t.Fatalf("profile log: %d", n)
		}
		// Пустой профиль — NULL ($profile ?: null); запись в журнал — при каждом сохранении.
		expect(t, e.send("PUT", owner, "/api/settings/profile", map[string]any{"description": "  ", "phone": nil}), http.StatusOK, "empty")
		if e.text("SELECT profile::text FROM clubs WHERE id = $1", e.club) != "<null>" || e.activityCount("club.profile_changed", 0) != 2 {
			t.Fatal("empty profile")
		}
		expect(t, e.send("PUT", cashier, "/api/settings/profile", map[string]any{"description": "x"}), http.StatusForbidden, "cashier")
	})

	t.Run("GS-570 photos: upload, cover, delete, public address", func(t *testing.T) {
		img1, img2 := pngImage(t, 320, 240), pngImage(t, 640, 480)
		r := e.uploadPhotos(owner, img1, img2)
		expect(t, r, http.StatusOK, "photos")
		if r.body["message"] != "Фото добавлены." {
			t.Fatalf("message: %v", r.body)
		}
		if e.scalar("SELECT count(*) FROM club_photos WHERE club_id = $1", e.club) != 2 ||
			e.scalar("SELECT count(*) FROM club_photo_images WHERE club_id = $1 AND content_type = 'image/png'", e.club) != 2 ||
			e.scalar("SELECT min(sort_order) FROM club_photos WHERE club_id = $1", e.club) != 1 ||
			e.scalar("SELECT max(sort_order) FROM club_photos WHERE club_id = $1", e.club) != 2 {
			t.Fatal("photo rows")
		}
		if d := e.text("SELECT description FROM activity_logs WHERE club_id = $1 AND event = 'club.photos_added'", e.club); d != "Добавлено 2 фото клуба" {
			t.Fatalf("photos log: %q", d)
		}
		pathRe := regexp.MustCompile(fmt.Sprintf(`^club-photos/%d/[A-Za-z0-9]{40}\.png$`, e.club))
		if p := e.text("SELECT path FROM club_photos WHERE club_id = $1 ORDER BY sort_order LIMIT 1", e.club); !pathRe.MatchString(p) {
			t.Fatalf("path %q", p)
		}

		// Не картинка — ошибки правил mimes и dimensions (тексты валидатора — английские, как в старом).
		r = e.uploadPhotos(owner, []byte("not an image"))
		expectField(t, r, "photos.0", "The photos.0 field must be a file of type: jpg, jpeg, png, webp.")
		if list, _ := obj(r.body["errors"])["photos.0"].([]any); len(list) != 2 || list[1] != "The photos.0 field has invalid image dimensions." {
			t.Fatalf("errors: %v", r.body)
		}
		expectField(t, e.uploadPhotos(owner, img1, pngImage(t, 100, 300)), "photos.1", "The photos.1 field has invalid image dimensions.")
		expectField(t, e.uploadPhotos(owner, webpImage(6001, 300)), "photos.0", "The photos.0 field has invalid image dimensions.")
		expectField(t, e.uploadPhotos(owner), "photos", "The photos field is required.")
		big := append(pngImage(t, 320, 240), make([]byte, 3072*1024)...)
		expectField(t, e.uploadPhotos(owner, big), "photos.0", "Фото — не больше 3 МБ.")
		if e.scalar("SELECT count(*) FROM club_photos WHERE club_id = $1", e.club) != 2 {
			t.Fatal("photos saved despite errors")
		}

		// WebP принимается (в отличие от логотипа); расширение и тип — по содержимому.
		expect(t, e.uploadPhotos(owner, webpImage(800, 600)), http.StatusOK, "webp")
		if e.scalar("SELECT count(*) FROM club_photos p JOIN club_photo_images i ON i.photo_id = p.id WHERE p.club_id = $1 AND p.path LIKE '%.webp' AND i.content_type = 'image/webp' AND p.sort_order = 3", e.club) != 1 {
			t.Fatal("webp photo")
		}

		// Список: первое по sort_order — обложка.
		photos, _ := e.get(owner, "/api/settings/profile").body["photos"].([]any)
		if len(photos) != 3 {
			t.Fatalf("photos: %v", photos)
		}
		first, second := obj(photos[0]), obj(photos[1])
		firstID, secondID := int64(first["id"].(float64)), int64(second["id"].(float64))
		secondURL, _ := second["url"].(string)
		if !strings.HasPrefix(secondURL, fmt.Sprintf("/club-photos/%d/", e.club)) {
			t.Fatalf("url %q", secondURL)
		}

		// «Сделать обложкой»: вторая становится первой (sort_order = min − 1); в журнал не пишется.
		before := e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1", e.club)
		r = e.send("POST", owner, fmt.Sprintf("/api/settings/profile/photos/%d/cover", secondID), nil)
		expect(t, r, http.StatusOK, "cover")
		if r.body["message"] != "Обложка клуба изменена." || e.scalar("SELECT id FROM club_photos WHERE club_id = $1 ORDER BY sort_order, id LIMIT 1", e.club) != secondID ||
			e.scalar("SELECT sort_order FROM club_photos WHERE id = $1", secondID) != 0 {
			t.Fatalf("cover: %v", r.body)
		}
		if e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1", e.club) != before {
			t.Fatal("cover must not be logged")
		}
		// Ещё раз обложкой первую: порядок уходит ниже нуля (в MySQL — беззнаковая колонка; здесь integer).
		expect(t, e.send("POST", owner, fmt.Sprintf("/api/settings/profile/photos/%d/cover", firstID), nil), http.StatusOK, "cover again")
		if e.scalar("SELECT sort_order FROM club_photos WHERE id = $1", firstID) != -1 {
			t.Fatal("negative sort order")
		}
		expect(t, e.send("POST", owner, fmt.Sprintf("/api/settings/profile/photos/%d/cover", otherPhoto), nil), http.StatusNotFound, "foreign cover")

		// Фото по адресу — без входа; другое имя (40 символов) — 404.
		res, err := http.Get(e.srv.URL + secondURL) // тест
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 200 || !bytes.Equal(body, img2) || res.Header.Get("Content-Type") != "image/png" ||
			res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" ||
			res.Header.Get("Content-Security-Policy") != "default-src 'none'" {
			t.Fatalf("public photo: %d %v", res.StatusCode, res.Header)
		}
		for _, name := range []string{strings.Repeat("a", 40) + ".png", "short.png", strings.Repeat("a", 40) + ".gif", strings.Repeat("c", 40) + ".png"} {
			if res, _ := http.Get(e.srv.URL + fmt.Sprintf("/club-photos/%d/%s", e.club, name)); res.StatusCode != 404 { // тест
				t.Fatalf("%s: %d", name, res.StatusCode)
			}
		}

		// Удаление: строки и байтов нет, адрес — 404.
		expect(t, e.send("DELETE", manager, fmt.Sprintf("/api/settings/profile/photos/%d", secondID), nil), http.StatusOK, "delete")
		if e.scalar("SELECT count(*) FROM club_photos WHERE id = $1", secondID) != 0 || e.scalar("SELECT count(*) FROM club_photo_images WHERE photo_id = $1", secondID) != 0 ||
			e.activityCount("club.photo_deleted", 0) != 1 {
			t.Fatal("photo not deleted")
		}
		if res, _ := http.Get(e.srv.URL + secondURL); res.StatusCode != 404 { // тест
			t.Fatalf("deleted photo: %d", res.StatusCode)
		}
		expect(t, e.send("DELETE", owner, fmt.Sprintf("/api/settings/profile/photos/%d", secondID), nil), http.StatusNotFound, "delete twice")
		expect(t, e.send("DELETE", owner, fmt.Sprintf("/api/settings/profile/photos/%d", otherPhoto), nil), http.StatusNotFound, "foreign delete")
		if e.scalar("SELECT count(*) FROM club_photos WHERE id = $1", otherPhoto) != 1 {
			t.Fatal("foreign photo deleted")
		}
		expect(t, e.uploadPhotos(cashier, img1), http.StatusForbidden, "cashier upload")
	})

	t.Run("photos: limit of 10 per club and large uploads", func(t *testing.T) {
		// Сейчас 2 фото. 11 файлов в форме — правило max:10; 9 файлов — больше лимита клуба.
		small := pngImage(t, 200, 200)
		many := make([][]byte, 11)
		for i := range many {
			many[i] = small
		}
		expectField(t, e.uploadPhotos(owner, many...), "photos", "The photos field must not have more than 10 items.")
		expectField(t, e.uploadPhotos(owner, many[:9]...), "photos", "У клуба может быть не больше 10 фото.")
		// 4 файла по ~2,5 МБ одной формой (больше общего предела multipart 8 МБ): свой предел маршрута.
		large := noisePNG(t, 800, 800)
		if len(large) < 2<<20 || len(large) > 3072*1024 {
			t.Fatalf("large photo size %d", len(large))
		}
		expect(t, e.uploadPhotos(owner, large, large, large, large), http.StatusOK, "large")
		expect(t, e.uploadPhotos(owner, small, small, small, small), http.StatusOK, "fill")
		if e.scalar("SELECT count(*) FROM club_photos WHERE club_id = $1", e.club) != 10 {
			t.Fatal("ten photos")
		}
		expectField(t, e.uploadPhotos(owner, small), "photos", "У клуба может быть не больше 10 фото.")
	})

	t.Run("GS-570 zone equipment", func(t *testing.T) {
		r := e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", zone), map[string]any{"description": "Тихая комната",
			"devices": map[string]any{"monitor": "ASUS 240 Гц", "gpu": "", "mouse": " Logitech G102 ", "unknown": "x"}})
		expect(t, r, http.StatusOK, "zone")
		if r.body["message"] != "Зона VIP сохранена." {
			t.Fatalf("message: %v", r.body)
		}
		var devices map[string]string
		if err := json.Unmarshal([]byte(e.text("SELECT devices::text FROM club_zones WHERE id = $1", zone)), &devices); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(devices) != fmt.Sprint(map[string]string{"monitor": "ASUS 240 Гц", "mouse": "Logitech G102"}) ||
			e.text("SELECT description FROM club_zones WHERE id = $1", zone) != "Тихая комната" {
			t.Fatalf("zone: %v", devices)
		}
		if e.scalar("SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'club.zone_devices_changed' AND subject_id = $2 AND description = 'Оборудование зоны VIP изменено'", e.club, zone) != 1 {
			t.Fatal("zone log")
		}
		z := obj(e.get(owner, "/api/settings/profile").body["zones"].([]any)[0])
		if d := obj(z["devices"]); d["monitor"] != "ASUS 240 Гц" || d["mouse"] != "Logitech G102" || len(d) != 2 || z["description"] != "Тихая комната" {
			t.Fatalf("zone view: %v", z)
		}

		expectField(t, e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", zone), map[string]any{"devices": map[string]any{"cpu": strings.Repeat("я", 121)}}),
			"devices.cpu", "The devices.cpu field must not be greater than 120 characters.")
		expectField(t, e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", zone), map[string]any{"description": strings.Repeat("я", 501)}),
			"description", "The description field must not be greater than 500 characters.")
		// Описание до 500 символов (в MySQL колонка была 255 — ошибка базы; здесь varchar(500)).
		expect(t, e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", empty), map[string]any{"description": strings.Repeat("я", 500)}), http.StatusOK, "500")

		// Пусто — NULL и в описании, и в оборудовании.
		expect(t, e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", zone), map[string]any{"description": "", "devices": map[string]any{"cpu": " "}}), http.StatusOK, "clear")
		if e.text("SELECT devices::text FROM club_zones WHERE id = $1", zone) != "<null>" || e.text("SELECT description FROM club_zones WHERE id = $1", zone) != "<null>" {
			t.Fatal("zone not cleared")
		}
		expect(t, e.send("PUT", owner, fmt.Sprintf("/api/settings/profile/zones/%d", otherZone), map[string]any{"description": "x"}), http.StatusNotFound, "foreign zone")
		expect(t, e.send("PUT", cashier, fmt.Sprintf("/api/settings/profile/zones/%d", zone), map[string]any{"description": "x"}), http.StatusForbidden, "cashier zone")
		if e.text("SELECT description FROM club_zones WHERE id = $1", otherZone) != "<null>" {
			t.Fatal("foreign zone changed")
		}
	})
}
