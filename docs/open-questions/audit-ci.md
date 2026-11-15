# Вопросы аудита — CI, выкладка, документация (исполнитель F3)

Находки: `A-03`, nginx-часть `A-05` (безопасность); `BC-02`, `BC-09`, `BC-10`, `BC-19` (код бэкенда); nginx-часть
`PB-11` (производительность); CI-часть `FE-11` (фронт). Файлы: `.github/**`, `deploy/staging/nginx/**`,
`deploy/staging/nginx-test.sh`, `tools/versions.env`, `tools/check-versions.sh`, `tools/bundle-budget.mjs`,
`Taskfile.yml`, `Dockerfile`, `.dockerignore`, `docs/deploy.md`, `docs/dev-setup.md`, `docs/agent-protocol.md`.

Решения приняты по принципам `docs/DECISIONS.md`; отменить может только владелец.

## Принято и сделано

- [x] **AC-1. Действия GitHub — по SHA коммита** (A-03). Все 56 строк `uses:` в 8 workflow (было 52 в 7):
  `owner/action@<sha> # vX.Y.Z`. Версия — последняя в прежней мажорной ветке, выпущенная ≥ 14 дней назад (PD-4;
  дата — дата коммита, на который указывает тег, получена `git fetch` этого коммита 15.11.2026):

  | Действие | Было | Стало | Дата |
  |---|---|---|---|
  | `actions/checkout` | `v7` | `3d3c42e5aac5ba805825da76410c181273ba90b1` v7.0.1 | 17.07.2026 |
  | `actions/setup-go` | `v7` | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` v7.0.0 | 15.07.2026 |
  | `actions/setup-node` | `v7` | `949feb2413d6458794dcd2491c4babbbce0c15c1` v7.1.0 | 07.10.2026 |
  | `actions/setup-dotnet` | `v6` | `a98b56852c35b8e3190ac28c8c2271da59106c68` v6.0.0 | 15.07.2026 |
  | `actions/cache` | `v4` | `0057852bfaa89a56745cba8c7296529d2fc39830` v4.3.0 | 24.09.2025 |
  | `actions/upload-artifact` | `v4` | `ea165f8d65b6e75b540449e92b4886f43607fa02` v4.6.2 | 19.03.2025 |
  | `actions/download-artifact` | `v4` | `d3f86a106a0bac45b974a628896c90dbdf5c8093` v4.3.0 | 24.04.2025 |
  | `golangci/golangci-lint-action` | `v9` | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` v9.3.0 | 29.06.2026 |
  | `docker/setup-buildx-action` | `v4` | `f87e5991a6d7451dcb8d9637bfbc97413f497069` v4.4.1 | 16.09.2026 |
  | `docker/build-push-action` | `v7` | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` v7.4.0 | 15.09.2026 |
  | `docker/login-action` | `v4` | `81c6f93d4bc991e510b342a00b419b3bb4efc5c6` v4.7.0 | 23.10.2026 |
  | `sigstore/cosign-installer` | `v4` | `6f9f17788090df1f26f669e9d70d6ae9567deba6` v4.1.2 | 06.05.2026 |

  Не взяты как слишком свежие (< 14 дней): `setup-dotnet` v6.1.0 (02.11.2026), `login-action` v4.8.0 (05.11.2026),
  `cosign-installer` v4.1.3 (04.11.2026) — их предложит Dependabot после cooldown.
  Попутно: у `sigstore/cosign-installer` **нет тега `v4`** (только `v4.x.y`) — прежняя строка
  `sigstore/cosign-installer@v4` не разрешилась бы, и задача `publish` упала бы при `COSIGN_SIGN=true`.
- [x] **AC-2. Версии инструментов — один файл `tools/versions.env`** (BC-09): goose был записан в 6 местах. Читают
  Taskfile (dotenv), workflow (шаг «versions»), Dockerfile (стадия `go`). `tools/check-versions.sh` в CI ловит дубли.
  Вариант аудита «`tools/go.mod` с директивами `tool`» не взят: общий граф зависимостей десяти инструментов меняет
  версии их зависимостей (MVS) относительно выпущенных бинарников и требует сети для проверки; зато Dependabot
  видел бы версии. Сейчас Dependabot файл не видит — обновление вручную (см. AC-O3).
- [x] **AC-3. Раннеры — конкретные образы**: `windows-latest` → `windows-2025`, `ubuntu-latest` → `ubuntu-24.04`
  (`agent.yml`, `agent-release.yml`). Node в CI — точная `22.23.3` (как образ в `Dockerfile`), была `22`.
- [x] **AC-4. CI проверяет генерацию и правило тенантности** (BC-02): задача `generated` в `go.yml` —
  `task gen:check:go` (`sqlc vet` с `tenant-filter`, `sqlc diff`, `buf lint`, `sqlc generate`, `api/gen.sh`,
  `buf generate` → дерево не меняется) и `buf breaking` против базовой ветки PR.
- [x] **AC-5. `buf breaking` можно обойти меткой PR `proto-breaking`.** Контракт агента ещё может меняться
  несовместимо до первого выпуска агента (решение 3 в `CLAUDE.md`); после выпуска — см. AC-O1.
- [x] **AC-6. `tools/proto-csharp-check.sh` удалён** (BC-19): нигде не вызывался; ту же проверку (генерация C# из
  `proto/agent/v1/*.proto` и компиляция клиентов всех шести сервисов) делает каждая сборка
  `agent/src/Zafiro.Agent.Protocol` (Grpc.Tools, `GrpcServices="Client"`), а `agent.yml` срабатывает на `proto/**`.
- [x] **AC-7. Dependabot** (BC-10): добавлены `deploy/backup/Dockerfile`, шесть compose-каталогов вне корня,
  `dotnet-sdk` для `agent/global.json`; cooldown 14 дней у всех.
- [x] **AC-8. nginx staging: сжатие и журнал без секретов** (PB-11, A-05) — `docs/deploy.md` §3. `gzip_static` не
  включён (на балансировщике нет файлов: статику отдаёт сервер из `embed.FS`). `Referer` из журнала убран целиком.
- [x] **AC-9. Бюджет размера фронта в CI** (FE-11): `tools/bundle-budget.mjs` после `npm run build`. Пороги —
  «текущее + ≈ 10 %» (байты gzip): `main` 92 000 (сейчас 83 462), начальная загрузка 160 000 (145 490), CSS 51 000
  (46 534), самый большой чанк по требованию 416 000 (378 398 — `apidocs`, справка `/docs/api`). Это защита от
  роста; работы по фронту (F4: FE-01…FE-12) должны **уменьшить** пороги до целей аудита: `main` ≤ 90 КБ, CSS ≤ 50 КБ,
  чанк страницы ≤ 30 КБ.

## Открытые (решает владелец)

- [ ] **AC-O1. `buf breaking` после первого выпуска агента.** Сейчас обход — метка PR. Когда агенты нового протокола
  стоят в клубах, несовместимое изменение ломает их до обновления.
  - **Рекомендация:** после первого выпуска убрать обход меткой (или оставить право ставить метку только владельцу).
- [ ] **AC-O2. `agent/global.json`: `rollForward: latestFeature`** (BC-09) — CI ставит самый свежий SDK 10.0.x, а не
  закреплённый. Теперь файл видит Dependabot (`dotnet-sdk`).
  - **Рекомендация:** записать точную версию SDK (сейчас подходит 10.0.402 от 21.10.2026) и `rollForward: disable`
    либо `latestPatch`; проверить сборку агента с `TreatWarningsAsErrors` (новые анализаторы SDK). Не сделано:
    файл в зоне агента, без прогона сборки на Windows менять рискованно.
- [ ] **AC-O3. Образ `mcr.microsoft.com/dotnet/sdk:10.0` в `agent/test.sh` — без точного тега и digest** (BC-09).
  - **Рекомендация:** `mcr.microsoft.com/dotnet/sdk:10.0.402@sha256:747cbade5bfd918a175ac32f8f1d931edec2c7d0d5397a3de72eaef1ac1f66d2`
    (образ от 21.10.2026, старше 14 дней; 10.0.403 и 10.0.404 — моложе). Dependabot shell-скрипты не читает —
    обновлять вместе с `global.json`.
- [ ] **AC-O4. Образы в workflow вне поля зрения Dependabot**: `postgres:18.6` (`services` в `go.yml`, без digest) и
  `TRIVY_IMAGE` (`release.yml`, тег + digest), а также `tools/versions.env`.
  - **Рекомендация:** раз в месяц — ручная проверка (чек-лист выпуска) либо Renovate с `regexManagers` для этих трёх
    мест; `postgres` в `go.yml` — добавить digest, как в `deploy/staging/compose.yaml`.
- [ ] **AC-O5. Журнал ошибок nginx** (`error_log … warn`) печатает строку запроса целиком (с токеном приглашения),
  когда upstream недоступен или запрос отклонён. nginx не умеет его форматировать.
  - **Рекомендация:** в проде не отправлять `error_log` балансировщика во внешние системы без фильтра
    (маскирование на сборщике логов по тем же префиксам), либо принять риск: строки редкие, токен одноразовый.
- [ ] **AC-O6. Устаревшие комментарии в файлах других зон** (после удаления `tools/proto-csharp-check.sh` и по BC-19):
  `buf.gen.yaml:5` («grpc v1.85.0» — в `server/go.mod` v1.84.0), `buf.gen.yaml:7`, `agent/Directory.Packages.props:4`,
  `agent/test.sh:14` (ссылки на удалённый скрипт), `api/README.md:40` (`sections/todos/section.go` — файл называется
  `todos.go`), текст «Проверьте строку schedule:run в crontab…» в `web/src/features/superadmin/DashboardPage.tsx:88`
  (FE-18: совет про Laravel; формулировку решает владелец).

## Не проверено без запуска GitHub Actions

- Сами workflow не запускались: синтаксис проверен разбором YAML, команды шагов выполнены локально на копии `HEAD`
  (`task gen:check:go`, `task proto:breaking`, `tools/check-versions.sh`, `tools/bundle-budget.mjs`,
  `deploy/staging/nginx-test.sh`). `actionlint` локально не запускался.
- Метка раннера `windows-2025` и сборка агента на нём (раньше `windows-latest`); установка Node `22.23.3`
  действием `setup-node`; чтение `tools/versions.env` на Windows (в шаге стоит `tr -d '\r'` на случай CRLF).
- `actions/setup-dotnet` v6.0.0 вместо плавающего `v6` (= v6.1.0): на одну минорную версию назад.
- Второй `actions/checkout` базовой ветки в `.buf-base` (sparse) для `buf breaking`; локально то же проверено на
  каталоге с `proto/` и `buf.yaml` из `origin/main`.
- Dependabot: распознаёт ли он образы вида `${VAR:-образ:тег@sha256:…}` в compose-файлах и `ARG X=образ` в
  `Dockerfile`, файлы `compose.backup.yml` / `compose.load.yaml`, экосистему `dotnet-sdk` — видно только по его PR.
- Полная сборка образа `Dockerfile` после правки стадии `go` (чтение `GOOSE_VERSION` из `tools/versions.env`):
  проверены `docker build --check`, содержимое контекста (`.dockerignore` пропускает из `tools/` только
  `versions.env`) и чтение файла оболочкой alpine; сам образ не собирался (мало диска на общем стенде).
