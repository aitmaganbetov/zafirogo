// Package worker — фоновые задачи вместо cron Laravel (routes/console.php): истечение сессий,
// метрики, подписки, очистка. Не запускаются дважды на нескольких инстансах
// (лидер или JetStream-консьюмер): истечение сессий (ExpireSessions), сгорание пакетов (BurnPackages).
package worker
