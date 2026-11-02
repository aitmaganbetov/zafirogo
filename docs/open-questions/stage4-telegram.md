# Этап 4, H2 — Telegram: решения и отличия

Решения приняты по принципам `docs/DECISIONS.md` (владелец поручил). Код: `server/internal/telegram` (клиенты Gateway и
Bot API, настройки), `server/internal/verifycode` (коды восстановления пароля и перевода), `server/internal/monitoring`
(`alerts.go`, `checks.go`), `server/internal/http/sections/recovery` (`api/recovery.yaml`), `server/internal/agent/guestapi/telegram.go`,
кабинет суперадмина `sections/superadmin/telegram.go`, связка — `server/internal/app/telegram.go`.

## TG-1. Лимиты кодов атомарны (отличие, принцип 4)

- [x] Решено: отправки кодов одного аккаунта идут по очереди (`SELECT … FROM users … FOR UPDATE`), запись кода
  резервируется до вызова Telegram и удаляется, если Telegram код не принял.

В старом `PasswordRecovery::sendCode()` и `FriendTransferCodes::send()` проверка «60 с / 5 в час» и запись кода не были
атомарны: два параллельных запроса проходили оба (два платных кода). Видимое поведение то же: неотправленный код лимит
не тратит (как в старом — записи не было), тексты и поля ошибок прежние.

## TG-2. «Планировщик молчит» — воркер на каждом инстансе

- [x] Решено: `monitoring-watchdog` — каждый инстанс раз в минуту сравнивает последнюю минуту `server_metrics` с часами
  (> 5 минут — оповещение `scheduler`), без лидерства.

В старом проверку делали HTTP-запросы (`SchedulerWatchdog::check()`), потому что cron мог пропасть целиком. В Go
планировщика нет: снимки пишет лидер (`monitoring:collect`); если он завис, оповестит любой живой инстанс. Текст —
старый ключ перевода («…Проверьте crontab.»), не меняем (ключи переводов прежние, Q-065).

## TG-3. Состояние оповещений — Valkey

- [x] Решено (Q-060): `SET NX` при начале проблемы, `DEL` при окончании (`monitoring.Alerts`), ключ
  `zafiro:monitoring.alert.<ключ>`. Один переход — одно сообщение даже при нескольких инстансах. Бот не подключён —
  состояние ведётся, сообщений нет (как `Alerts::update()`).

Условия перенесены из `CollectServerMetrics` (диск > 85 %, 5xx ≥ 10 и ≥ 5 % за 5 минут, очередь ≥ 100 или старше
10 минут), `SnapshotClubLoad` (все ПК клуба с активной подпиской ушли офлайн, было ≥ 3) и `SchedulerWatchdog`.
Очереди Laravel в новом сервере нет (`QueuePending` = 0) — условие перенесено, но не срабатывает. Сертификаты
оповещений не слали (PD-5) — не добавляем.

## TG-4. Веб-восстановление пароля — свой раздел REST

- [x] Решено: `GET/POST /api/forgot-password`, `POST /api/reset-password` (раздел `recovery`), лимит `password-recovery`.
  Телефон между шагами хранит браузер (sessionStorage), поэтому смена пароля принимает `phone` в теле (как
  `POST /api/auth/password/reset` старого API).

Пути `/api/auth/password/code` и `/api/auth/password/reset` (старый REST агента и `mobileemployee`) оставлены слою
совместимости мобильных приложений (H3): там нужен формат ответа 1:1 (`message`, `code_minutes`, `resend_seconds`).
Логика общая — `verifycode.PasswordRecovery` (`httpx.Deps.PasswordRecovery`); так же `verifycode.TransferCodes.Send`
для `POST /api/guest/friends/{friendship}/transfer/code`.

## TG-5. «Выход везде» после смены пароля по коду

- [x] Решено: `PasswordRecovery.SignOut` (app: `passwordResetSignOut`) отзывает сеансы панели (`session.RevokeUser`) и
  токены гостя на ПК **всех** клубов (`Tokens.RevokeEverywhere`) с командой `SignOutGuest` на эти ПК.

Старый код удалял все Sanctum-токены (`$user->tokens()->delete()`) и строки `sessions`. Токены мобильных приложений
(H3, `0024_mobile_api_tokens`) нужно добавить в тот же хук `passwordResetSignOut` — **сделать при слиянии H3**.

## TG-6. Ошибки gRPC

- [x] Решено: «не подключено» — `FAILED_PRECONDITION` + `FEATURE_DISABLED`; «Новый код можно запросить через N с.» и
  «Слишком много запросов кода» — `RESOURCE_EXHAUSTED` + `RATE_LIMITED` с `retry_after_seconds`; неверный код —
  `INVALID_ARGUMENT` + `CONFIRMATION_CODE_INVALID`; кода нет, истёк или 5 попыток — `FAILED_PRECONDITION` +
  `CONFIRMATION_CODE_EXPIRED`; Telegram не принял номер — `FAILED_PRECONDITION` + `VALIDATION_FAILED` (поле phone/code).
  Тексты — старые (ключи перевода). Ошибки перевода (сумма, дружба, баланс) — как у `TransferBalance`.

## TG-7. Мелкие отличия

- [x] `ResetPassword` (gRPC) отклоняет пароль длиннее 255 символов (в старом ограничения не было; Argon2id длинный
  пароль не нужен, текст «Пароль — не длиннее 255 символов.» уже есть в агенте).
- [x] Хэш кода — Argon2id (`password.Hasher`), проверка принимает и перенесённые bcrypt (`Hash::make` старого).
- [x] Язык оповещений — язык запроса суперадмина, подключившего бота (`h.d.Locale`), как `app()->getLocale()` в
  `TelegramController::updateAlerts()`; проверочное сообщение при подключении и кнопка «Проверка» — тоже на нём.
- [x] Внешние вызовы — через `telegram.Caller` (`GatewayClient`, `BotClient`), таймаут 10 с, токен и номер в лог не
  попадают (ошибка связи логируется только видом: timeout/connection). Тесты — `telegram/telegramtest` (httptest).
