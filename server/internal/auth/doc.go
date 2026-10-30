// Package auth — аутентификация и авторизация админки:
//   - password — Argon2id и проверка старых bcrypt-хэшей Laravel с перехешем при входе;
//   - session — короткий access-токен (HMAC) и ротация refresh-токена в Valkey;
//   - permissions — роли и права как в Laravel (ClubPermissions, ClubRoles, StaffPermissions) и middleware;
//   - ratelimit — именованные лимиты throttle: в Valkey;
//   - principal — кто выполняет запрос (контракт для остальных пакетов).
//
// Этот пакет связывает их с Postgres: вход по телефону (AuthController::login), текущий пользователь,
// выбор клуба, режим просмотра клуба суперадмином, middleware Principal и TenantTx.
package auth
