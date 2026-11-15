# Перенос темы Ж: агент — вход по коду брони и запуск оплаченной брони вошедшего гостя (ноябрь 2026)

Исполнитель П6 (`docs/upstream-2026-11.md`, тема Ж, §8; правило BR-178; сценарии GS-544, GS-551; решения UQ-11, UQ-12,
UQ-14, UQ-15, UQ-24). Источник — старый Laravel `a680bab`: `Api/AgentBookingCodeController.php`,
`ComputerReservations::startByCode()` / `startForSignedIn()`, `AppServiceProvider` (лимит `agent-booking-code`),
`GuestHistory.php`; старый агент — коммит `0da191b`: `WindowsAgent/ZafiroAgent/Views/ClubSetupWindow.xaml(.cs)`,
`Services/ClubSetupService.cs`, `ZafiroApiClient.cs`, `Service/VaultProtocol.cs`, `Frontend/i18n/{ru,kk,en}.json`.
Серверные правила броней — тема Г (`docs/open-questions/upstream-bookings.md` §3 — контракт).

## 1. Что перенесено

| Старое | Новое |
|---|---|
| `POST /api/agent/booking-code` (`agent.device`, `throttle:agent-booking-code`) | RPC `GuestAuthService.StartBookingByCode` (`proto/agent/v1/guest.proto`), обработчик `server/internal/agent/guestapi/booking.go` → `reservations.StartByCode`; лимит `ratelimit.AgentBookingCode` |
| ответ `{message, session: sessionPayload()}` | `StartBookingByCodeResponse{session}` — `ActiveSession{guest: true, user_id: 0}`; плюс команда `StartSession` после коммита |
| 422 поля `code` (5 текстов) | типизированные коды `common.proto`: `VALIDATION_FAILED` (формат), `BOOKING_CODE_NOT_FOUND`, `BOOKING_CODE_OTHER_COMPUTER`, `BOOKING_NOT_STARTED`, `COMPUTER_BUSY`; тексты — старые, на языке экрана (`LocalizeErrors`) |
| heartbeat с токеном клиента → `startForSignedIn()` | `guestapi.signIn` (общий для `Login`, `Register`, QR) → `startBookingOnSignIn` → `reservations.StartForSignedIn`; сессия — в `GuestSignIn.session`. Гость, вошедший раньше интервала, — задача `app-bookings:start` (тема Г) |
| `GuestHistory` — `reservation.paid`, `reservation.refunded` | `agent/guestapi/history.go` (свои и полученные события, фильтр «Покупки») |
| `ClubSetupWindow`: `BookingToggle`, `BookingCodePanel`, `SetBookingCode()`, `EnterBookingCodeAsync()`, `ApplyTexts()` | `agent/src/Zafiro.Agent.UI/Frontend/index.html` (`loginBookingPanel`, `loginBookingToggle`, обёртка `loginPhonePanel`), `lock.js` (режим `bookingCode`), `lock.css` (`.lock-booking-code` — 22 px, как `FontSize="22"`) |
| `ClubSetupService.EnterBookingCodeAsync`, `ZafiroApiClient.BookingCodeEndpoint` | `agent/src/Zafiro.Agent.Core/Guest/GuestApi.cs` `StartBookingByCodeAsync`; IPC — `IpcMethods.cs`, `IpcDispatcher.cs` (`GuestAuthService/StartBookingByCode`) |
| `VaultProtocol.RelayPath` (+ `agent/booking-code`), `DeviceVaultTests` | не нужно: в новом агенте UI в сеть не ходит вовсе, служба зовёт RPC по mTLS; список разрешённых методов — таблица IPC (`IpcMethods`), тест `Таблица_IPC_совпадает_с_методами_службы` |
| 7 ключей перевода × ru/kk/en | уже были во встроенных переводах агента (`Frontend/i18n`); используются все семь + «Вернуться ко входу» |

Других изменений в `WindowsAgent/` за диапазон `zafirogo-base..a680bab` нет (9 файлов, все — про код брони).

## 2. Решения и отличия от старого

| # | Старое | Новое | Почему |
|---|---|---|---|
| UA-1 | окно входа после успеха само опрашивает сессию (`CheckGuestSessionAsync` → `ApplyGuestSession`) | сессия приходит в ответе RPC, служба применяет её сразу (`AgentState.StartSession`), UI получает снимок; сервер дублирует её командой `StartSession` | push вместо опроса; команда долговременная — потерянный ответ RPC не оставит оплаченную сессию на закрытом ПК |
| UA-2 | ошибки — текст поля `code` | код протокола + тот же текст | «агент решает по `ErrorCode`, а не по тексту» (`common.proto`); новые значения 92–95 добавлены в группу ошибок гостя |
| UA-3 | ссылка «Есть код брони?» видна на экране входа подключённого ПК всегда | то же (признака «бронь из приложения включена» в `ClubConfig` нет) | в задании предполагалось, что старый агент показывает ссылку только при включённой настройке, — это не так: `BookingToggle.Visibility = Visible` при любом входе на подключённом ПК. Скрывать по настройке — отличие от старого и поломка: брони, оплаченные до выключения настройки, перестали бы открываться по коду. См. §3, вопрос 1 |
| UA-4 | подписка клуба у маршрута не проверялась отдельно (`agent.device`) | `SUBSCRIPTION_INACTIVE`, как у входа и QR | UQ-12: агент ПК без подписки клуба не работает |
| UA-5 | неудачные попытки нигде не видны | системный журнал сервера `booking code rejected` (клуб, ПК, причина; сам код не пишется), успех — `booking opened with a code` | по образцу неверного PIN выхода без вошедшего гостя; в журнал действий клуба отказы не идут (в старом их там нет, и писать не от чьего имени) |
| UA-6 | `startForSignedIn()` на каждом heartbeat | при входе гостя, отдельной транзакцией; ошибка запуска входу не мешает (журнал `paid booking not started on sign-in`), задача повторит в пределах минуты | UQ-11; бронь не должна ломать вход |
| UA-7 | история: у `reservation.paid` примечание — время начала | в `HistoryEntry` примечаний нет ни у одного события (страница агента рисует `note: null`) — время начала не передаётся | не расширять протокол ради одного события; в приложении (`legacy/guest.go`) примечание есть |
| UA-8 | заголовки истории — `__()` на языке запроса | русские, как у остальных событий `ListHistory` | существующее ограничение истории агента, не этой темы |
| UA-9 | служба не повторяет запрос кода | то же: `StartBookingByCode` не идемпотентен по смыслу (повтор после успеха — «не найден»), повторов в `GuestApi` нет; от потери ответа защищает `StartSession` | лимит 5 попыток в минуту — повторы съели бы его |

### Безопасность

- **Перебор**: 6 цифр, 5 попыток в минуту на ПК и 200 в час на клуб (Valkey, ключ — `computer_id` и `club_id` из
  сертификата); шестая попытка отклоняется даже с верным кодом (`TestBookingCodeRateLimit`). Ответы «не найден» и «не
  для этого ПК» различаются, как в старом (UQ-14) — это подтверждает существование кода, но открыть по нему можно
  только забронированный ПК.
- **Чужой клуб / чужой ПК**: клуб и ПК — только из сертификата mTLS; запросы фильтруют `club_id`, второй рубеж — RLS.
  Код клуба A на ПК клуба B — «не найден» (`TestBookingCode`).
- **Повтор кода**: после старта бронь не ожидающая — «не найден», сессия не меняется.
- **Гонка**: блокировки ПК → бронь, запуск идемпотентен по `arrived_at`; один код на двух забронированных ПК и на
  третьем, чужом, по 6 одновременных попыток с каждого — ровно по одному старту на забронированных, ни одного на
  чужом (`TestBookingCodeRace`).
- Код брони в журналы не пишется; токена гостя RPC не выдаёт.

### Сессия без аккаунта в агенте

Как время, проданное на кассе без аккаунта (уже было): `ActiveSession.guest`, гостя в снимке нет → экран скрыт, ПК
открыт, лаунчер сам не открывается (нет магазина, баланса, профиля, чата); предупреждения за 10/5/3/1 минуту — в углу;
конец времени — экран входа; свежая загрузка ПК сбрасывает сессию (`Hooks.FreshBoot`, GS-077). Нового кода для этого
не понадобилось — тесты `BookingCodeTests` фиксируют поведение.

## 3. Что не сделано и вопросы владельцу

1. **Показывать ли «Есть код брони?» только в клубах с включённой бронью из приложения** (UA-3). Сейчас — всегда, как в
   старом. Если нужно скрывать: поле в `ClubConfig` + условие на странице; тогда решить, что делать с бронями,
   оплаченными до выключения настройки.
2. **Ручная проверка на Windows** — пункт «Вход по коду брони» в `docs/agent-protocol.md` §14.9 (машины нет).
   Разметка режима проверена тестами страницы без WebView2; пиксельного сравнения со старым WPF-окном нет (как и у
   остального экрана входа — он перенесён из XAML в HTML по виду).
3. **Примечание «время начала» в истории агента** (UA-7) и **язык заголовков истории** (UA-8) — если нужны, это
   расширение `HistoryEntry` для всех событий, отдельной задачей.
4. **Ярлык «Zafiro» во время сессии без аккаунта** открывает лаунчер без гостя — так же, как при времени с кассы;
   поведение старого агента в этом случае не сверялось (вне темы).

## 4. Тесты

- Go unit: `guestapi` `TestBookingCodeStatus` (коды, тексты, поле), `TestHistoryEntryBooking`.
- Go интеграционные (`-tags=integration`, Postgres + Valkey + NATS, настоящий gRPC с mTLS): `TestBookingCode`,
  `TestBookingCodeSubscriptionInactive`, `TestBookingCodeRateLimit`, `TestBookingCodeRace`,
  `TestSignInStartsPaidBooking`, `TestQrLoginStartsPaidBooking`, `TestHistoryBookingEvents`.
- Агент .NET unit (`agent/test.sh unit`): `BookingCodeTests` — служба, типизированные отказы, IPC, мост страницы, экран
  и истечение сессии без аккаунта.
- Страница (`agent/test.sh frontend`): `tests/frontend/lock.test.mjs` — разметка, режим, только цифры, формат без
  запроса, успех, тексты отказов.
- Сквозной (`agent/test.sh integration`): `ServerScenarioTests.Вход_по_коду_брони` — клуб включает бронь из
  приложения → гость бронирует ПК через REST мобильных → код на ПК → сессия без аккаунта → `StartSession` → повтор.
