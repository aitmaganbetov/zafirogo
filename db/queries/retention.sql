-- Построчная очистка устаревших строк (воркер data:retention, worker.DataRetention): платформенные запросы по всем
-- клубам, пул zafiro_platform, один исполнитель на кластер (Q-086). Каждый запрос удаляет одну пачку не больше
-- batch строк и возвращает их число — воркер повторяет его, пока пачка полная (как PruneIntegrationData::prune()
-- старого проекта: limit(1000), «so a large backlog never holds long locks»). Транзакция — одна пачка.
--
-- Клубы в состоянии imported (перенесены из MySQL и ещё не открыты, 0027) не чистятся: их данные сверяет инструмент
-- переноса, а работает клуб пока в старом Laravel, который чистит свою копию сам.
-- Журналы с месячными партициями (api_request_logs, история здоровья ПК) удаляются целыми партициями
-- (worker.LogRetention), а не здесь.

-- name: RetentionDeleteApiIdempotencyKeys :execrows
-- Laravel: app/Console/Commands/PruneIntegrationData.php handle() — api_idempotency_keys с created_at старше
-- integrations.idempotency_retention_hours (24 ч). Тот же срок проверяет запрос с ключом (v1 middleware: строка старше
-- срока — новый запрос), поэтому ключ в окне повторов не удаляется. Индекс api_idempotency_keys_created_at_index.
DELETE FROM api_idempotency_keys
WHERE id IN (
    SELECT k.id
    FROM api_idempotency_keys k
    WHERE k.created_at < sqlc.arg(before)
      AND k.club_id IN (SELECT c.id FROM clubs c WHERE c.migration_state = 'live')
    ORDER BY k.created_at
    LIMIT sqlc.arg(batch)
);

-- name: RetentionDeleteWebhookDeliveries :execrows
-- Laravel: app/Console/Commands/PruneIntegrationData.php handle() — webhook_deliveries в статусах delivered, failed,
-- skipped с created_at старше integrations.webhook_delivery_retention_days (30 дней); pending не трогаются.
-- По клубам — индекс webhook_deliveries_club_id_status_created_at_index (club_id, status, created_at).
DELETE FROM webhook_deliveries
WHERE id IN (
    SELECT d.id
    FROM clubs c
    CROSS JOIN LATERAL (
        SELECT w.id
        FROM webhook_deliveries w
        WHERE w.club_id = c.id AND w.status IN ('delivered', 'failed', 'skipped') AND w.created_at < sqlc.arg(before)::timestamptz
        LIMIT sqlc.arg(batch)
    ) d
    WHERE c.migration_state = 'live'
    LIMIT sqlc.arg(batch)
);

-- name: RetentionDeleteSessionPurchases :execrows
-- Laravel: routes/console.php, Schedule::call(...)->daily()->name('session-purchases:prune') (BR-034) —
-- SessionPurchase::where('created_at', '<', now()->subDays(8))->whereNotIn('session_id', ClubComputer::
-- whereNotNull('current_session_id')->select('current_session_id'))->delete(): части сессий старше 8 дней, кроме
-- частей идущих сессий (длинная сессия не теряет части — их читает settle() при завершении). Части досрочно
-- завершённых сессий удаляет сам settle() (DeleteSessionPurchases); здесь — сессии, кончившиеся по времени (GAP-005).
-- Индекс session_purchases_created_at_index.
DELETE FROM session_purchases
WHERE id IN (
    SELECT sp.id
    FROM session_purchases sp
    WHERE sp.created_at < sqlc.arg(before)::timestamptz
      AND sp.session_id NOT IN (SELECT cc.current_session_id FROM club_computers cc WHERE cc.current_session_id IS NOT NULL)
      AND sp.club_id IN (SELECT c.id FROM clubs c WHERE c.migration_state = 'live')
    ORDER BY sp.created_at
    LIMIT sqlc.arg(batch)
);

-- name: RetentionDeleteAgentQrLogins :execrows
-- Laravel: routes/console.php, Schedule::call(...)->daily()->name('agent-qr-logins:prune') — AgentQrLogin::
-- where('expires_at', '<', now()->subDay())->delete() (QR живёт 5 минут). Индекс agent_qr_logins_expires_at_index.
DELETE FROM agent_qr_logins
WHERE id IN (
    SELECT q.id
    FROM agent_qr_logins q
    WHERE q.expires_at < sqlc.arg(before)
      AND q.club_id IN (SELECT c.id FROM clubs c WHERE c.migration_state = 'live')
    ORDER BY q.expires_at
    LIMIT sqlc.arg(batch)
);

-- name: RetentionDeleteEnrollmentCodes :execrows
-- Коды «Подключить ПК» старше 30 дней (решение A2 §8 «Очистка», docs/open-questions/stage2-agent-enroll.md; в старом
-- проекта кодов не было). Код, срок которого ещё не вышел (expires_at >= now), не удаляется. Ссылка из
-- agent_certificates.enrollment_code_id обнуляется (ON DELETE SET NULL). По клубам — индекс
-- agent_enrollment_codes_club_created_index (club_id, created_at).
DELETE FROM agent_enrollment_codes
WHERE id IN (
    SELECT e.id
    FROM clubs c
    CROSS JOIN LATERAL (
        SELECT a.id
        FROM agent_enrollment_codes a
        WHERE a.club_id = c.id AND a.created_at < sqlc.arg(before) AND a.expires_at < sqlc.arg(now)
        LIMIT sqlc.arg(batch)
    ) e
    WHERE c.migration_state = 'live'
    LIMIT sqlc.arg(batch)
);

-- name: RetentionDeleteAgentCertificates :execrows
-- Клиентские сертификаты агентов, истёкшие (not_after) больше 90 дней назад, действующие и отозванные (решение A2 §8
-- «Очистка»). Истёкший сертификат TLS не примет и без строки; кэш сертификатов узнаёт об удалении при полной
-- перезагрузке. По клубам — индекс agent_certificates_club_computer_index (club_id, club_computer_id, not_after),
-- пропуск по club_computer_id (skip scan, Postgres 18).
DELETE FROM agent_certificates
WHERE serial IN (
    SELECT e.serial
    FROM clubs c
    CROSS JOIN LATERAL (
        SELECT a.serial
        FROM agent_certificates a
        WHERE a.club_id = c.id AND a.not_after < sqlc.arg(before)
        LIMIT sqlc.arg(batch)
    ) e
    WHERE c.migration_state = 'live'
    LIMIT sqlc.arg(batch)
);
