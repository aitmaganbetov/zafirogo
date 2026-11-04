// Package enroll — регистрация ПК клуба и жизненный цикл клиентского сертификата агента:
//
//   - CreateCode / Reregister — сотрудник выдаёт одноразовый код «Подключить ПК» (право как
//     ClubComputerPolicy::register в Laravel: роли owner, admin, manager, operator);
//   - Enroll — ПК по коду получает сертификат (CSR → pki.CA.Issue), карточка ПК находится или
//     создаётся по правилам ClubComputerRegistrar (BR-124, MAC — ключ карточки, BR-127);
//   - RenewCertificate — продление под mTLS новой парой ключей; прежний сертификат отзывается
//     (superseded) при первом использовании нового или истекает сам;
//   - RevokeComputer — «Отключить ПК» / «Перерегистрировать»; удаление карточки отзывает
//     сертификаты триггером БД (0009_agent_enrollment.sql);
//   - CertCache — кэш действующих сертификатов в памяти инстанса (grpcserver.CertChecker).
//
// Laravel: app/Services/ClubComputerRegistrar.php, app/Support/MacAddress.php,
// app/Http/Controllers/Api/AgentEnrollController.php, app/Http/Middleware/AuthenticateAgentDevice.php.
package enroll
