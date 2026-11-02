-- Схема MySQL старого проекта Zafiro после всех миграций (php artisan migrate:fresh).
-- Сгенерировано tools/migrate-from-mysql/dump-schema.sh, не править руками.
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `activity_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `event` varchar(60) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `subject_type` varchar(100) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `subject_id` bigint unsigned DEFAULT NULL,
  `details` json DEFAULT NULL,
  `ip_address` varchar(45) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_agent` text COLLATE utf8mb4_unicode_ci,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `activity_logs_user_id_foreign` (`user_id`),
  KEY `activity_logs_created_at_event_index` (`created_at`,`event`),
  KEY `activity_logs_subject_type_subject_id_index` (`subject_type`,`subject_id`),
  KEY `activity_logs_event_index` (`event`),
  KEY `activity_logs_club_id_index` (`club_id`),
  KEY `activity_logs_club_id_created_at_index` (`club_id`,`created_at`),
  KEY `activity_logs_club_user_fk` (`club_id`,`user_id`),
  KEY `activity_logs_club_id_event_created_at_index` (`club_id`,`event`,`created_at`),
  CONSTRAINT `activity_logs_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `activity_logs_club_user_fk` FOREIGN KEY (`club_id`, `user_id`) REFERENCES `club_memberships` (`club_id`, `user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `agent_qr_logins` (
  `id` char(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `club_id` bigint unsigned NOT NULL,
  `club_computer_id` bigint unsigned NOT NULL,
  `code_hash` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `approved_user_id` bigint unsigned DEFAULT NULL,
  `approved_at` datetime DEFAULT NULL,
  `consumed_at` datetime DEFAULT NULL,
  `expires_at` datetime NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `agent_qr_logins_code_hash_unique` (`code_hash`),
  KEY `agent_qr_logins_club_id_foreign` (`club_id`),
  KEY `agent_qr_logins_club_computer_id_foreign` (`club_computer_id`),
  KEY `agent_qr_logins_approved_user_id_foreign` (`approved_user_id`),
  KEY `agent_qr_logins_expires_at_index` (`expires_at`),
  CONSTRAINT `agent_qr_logins_approved_user_id_foreign` FOREIGN KEY (`approved_user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE,
  CONSTRAINT `agent_qr_logins_club_computer_id_foreign` FOREIGN KEY (`club_computer_id`) REFERENCES `club_computers` (`id`) ON DELETE CASCADE,
  CONSTRAINT `agent_qr_logins_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `api_idempotency_keys` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `api_key_id` bigint unsigned NOT NULL,
  `idempotency_key` varchar(100) COLLATE utf8mb4_unicode_ci NOT NULL,
  `request_hash` char(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` smallint unsigned DEFAULT NULL,
  `response` longtext COLLATE utf8mb4_unicode_ci,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `api_idempotency_keys_api_key_id_idempotency_key_unique` (`api_key_id`,`idempotency_key`),
  KEY `api_idempotency_keys_created_at_index` (`created_at`),
  CONSTRAINT `api_idempotency_keys_api_key_id_foreign` FOREIGN KEY (`api_key_id`) REFERENCES `api_keys` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `api_keys` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `created_by` bigint unsigned NOT NULL,
  `name` varchar(100) COLLATE utf8mb4_unicode_ci NOT NULL,
  `prefix` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL,
  `key_hash` char(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `scopes` json NOT NULL,
  `allowed_ips` json DEFAULT NULL,
  `rate_limit_per_minute` smallint unsigned DEFAULT NULL,
  `expires_at` timestamp NULL DEFAULT NULL,
  `last_used_at` timestamp NULL DEFAULT NULL,
  `last_used_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `revoked_at` timestamp NULL DEFAULT NULL,
  `revoked_by` bigint unsigned DEFAULT NULL,
  `rotated_from_id` bigint unsigned DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `api_keys_prefix_unique` (`prefix`),
  KEY `api_keys_created_by_foreign` (`created_by`),
  KEY `api_keys_revoked_by_foreign` (`revoked_by`),
  KEY `api_keys_rotated_from_id_foreign` (`rotated_from_id`),
  KEY `api_keys_club_id_revoked_at_index` (`club_id`,`revoked_at`),
  CONSTRAINT `api_keys_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `api_keys_created_by_foreign` FOREIGN KEY (`created_by`) REFERENCES `users` (`id`),
  CONSTRAINT `api_keys_revoked_by_foreign` FOREIGN KEY (`revoked_by`) REFERENCES `users` (`id`),
  CONSTRAINT `api_keys_rotated_from_id_foreign` FOREIGN KEY (`rotated_from_id`) REFERENCES `api_keys` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `api_request_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned DEFAULT NULL,
  `api_key_id` bigint unsigned DEFAULT NULL,
  `key_prefix` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `method` varchar(8) COLLATE utf8mb4_unicode_ci NOT NULL,
  `endpoint` varchar(191) COLLATE utf8mb4_unicode_ci NOT NULL,
  `status` smallint unsigned NOT NULL,
  `duration_ms` int unsigned NOT NULL,
  `ip` varchar(45) COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `api_request_logs_club_id_created_at_index` (`club_id`,`created_at`),
  KEY `api_request_logs_api_key_id_created_at_index` (`api_key_id`,`created_at`),
  KEY `api_request_logs_created_at_index` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `cache` (
  `key` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `value` mediumtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `expiration` bigint NOT NULL,
  PRIMARY KEY (`key`),
  KEY `cache_expiration_index` (`expiration`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `cache_locks` (
  `key` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `owner` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `expiration` bigint NOT NULL,
  PRIMARY KEY (`key`),
  KEY `cache_locks_expiration_index` (`expiration`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `cash_shifts` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `opened_by` bigint unsigned DEFAULT NULL,
  `closed_by` bigint unsigned DEFAULT NULL,
  `status` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `open_marker` tinyint(1) DEFAULT NULL,
  `opening_balance` decimal(14,2) NOT NULL,
  `closing_balance` decimal(14,2) DEFAULT NULL,
  `opened_at` timestamp NOT NULL,
  `closed_at` timestamp NULL DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `cash_shifts_club_id_id_unique` (`club_id`,`id`),
  UNIQUE KEY `cash_shifts_club_id_open_marker_unique` (`club_id`,`open_marker`),
  KEY `cash_shifts_opened_by_foreign` (`opened_by`),
  KEY `cash_shifts_closed_by_foreign` (`closed_by`),
  KEY `cash_shifts_status_index` (`status`),
  KEY `cash_shifts_club_id_index` (`club_id`),
  KEY `cash_shifts_opened_by_membership_fk` (`club_id`,`opened_by`),
  KEY `cash_shifts_closed_by_membership_fk` (`club_id`,`closed_by`),
  CONSTRAINT `cash_shifts_closed_by_membership_fk` FOREIGN KEY (`club_id`, `closed_by`) REFERENCES `club_memberships` (`club_id`, `user_id`),
  CONSTRAINT `cash_shifts_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `cash_shifts_opened_by_membership_fk` FOREIGN KEY (`club_id`, `opened_by`) REFERENCES `club_memberships` (`club_id`, `user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `cash_transactions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `cash_shift_id` bigint unsigned NOT NULL,
  `refund_of_id` bigint unsigned DEFAULT NULL,
  `user_id` bigint unsigned DEFAULT NULL,
  `type` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `amount` decimal(14,2) NOT NULL,
  `description` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `receipt_data` json DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `cash_transactions_refund_of_id_unique` (`refund_of_id`),
  KEY `cash_transactions_cash_shift_id_foreign` (`cash_shift_id`),
  KEY `cash_transactions_user_id_foreign` (`user_id`),
  KEY `cash_transactions_type_index` (`type`),
  KEY `cash_transactions_club_id_index` (`club_id`),
  KEY `cash_transactions_club_shift_fk` (`club_id`,`cash_shift_id`),
  KEY `cash_transactions_club_user_fk` (`club_id`,`user_id`),
  KEY `cash_transactions_club_id_created_at_index` (`club_id`,`created_at`),
  CONSTRAINT `cash_transactions_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `cash_transactions_club_shift_fk` FOREIGN KEY (`club_id`, `cash_shift_id`) REFERENCES `cash_shifts` (`club_id`, `id`) ON DELETE CASCADE,
  CONSTRAINT `cash_transactions_club_user_fk` FOREIGN KEY (`club_id`, `user_id`) REFERENCES `club_memberships` (`club_id`, `user_id`),
  CONSTRAINT `cash_transactions_refund_of_id_foreign` FOREIGN KEY (`refund_of_id`) REFERENCES `cash_transactions` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `chat_messages` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL,
  `author_id` bigint unsigned DEFAULT NULL,
  `from_staff` tinyint(1) NOT NULL,
  `body` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `club_computer_id` bigint unsigned DEFAULT NULL,
  `read_at` datetime DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `chat_messages_customer_id_foreign` (`customer_id`),
  KEY `chat_messages_author_id_foreign` (`author_id`),
  KEY `chat_messages_club_computer_id_foreign` (`club_computer_id`),
  KEY `chat_messages_club_id_customer_id_id_index` (`club_id`,`customer_id`,`id`),
  KEY `chat_messages_club_id_from_staff_read_at_index` (`club_id`,`from_staff`,`read_at`),
  CONSTRAINT `chat_messages_author_id_foreign` FOREIGN KEY (`author_id`) REFERENCES `users` (`id`) ON DELETE SET NULL,
  CONSTRAINT `chat_messages_club_computer_id_foreign` FOREIGN KEY (`club_computer_id`) REFERENCES `club_computers` (`id`) ON DELETE SET NULL,
  CONSTRAINT `chat_messages_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `chat_messages_customer_id_foreign` FOREIGN KEY (`customer_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_computers` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_zone_id` bigint unsigned NOT NULL,
  `number` int unsigned NOT NULL,
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'available',
  `power_command` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `power_command_at` timestamp NULL DEFAULT NULL,
  `wake_requested_at` timestamp NULL DEFAULT NULL,
  `is_reserved` tinyint(1) NOT NULL DEFAULT '0',
  `position_x` int unsigned NOT NULL DEFAULT '5',
  `position_y` int unsigned NOT NULL DEFAULT '5',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `last_seen_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  `device_name` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `display_name` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `device_id` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_state` varchar(20) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_version` varchar(50) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_update_version` varchar(20) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_update_offers` tinyint unsigned NOT NULL DEFAULT '0',
  `agent_update_offered_at` timestamp NULL DEFAULT NULL,
  `current_session_id` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `current_session_user_id` bigint unsigned DEFAULT NULL,
  `session_guest` tinyint(1) NOT NULL DEFAULT '0',
  `session_ends_at` timestamp NULL DEFAULT NULL,
  `staff_user_id` bigint unsigned DEFAULT NULL,
  `staff_kind` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `staff_until` timestamp NULL DEFAULT NULL,
  `mac_address` varchar(17) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `local_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `remote_ip` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `cpu_model` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `cpu_cores` smallint unsigned DEFAULT NULL,
  `ram_total_mb` int unsigned DEFAULT NULL,
  `gpu_model` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `os_version` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `hardware_reported_at` timestamp NULL DEFAULT NULL,
  `agent_token_hash` char(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_token_issued_at` timestamp NULL DEFAULT NULL,
  `staff_message` varchar(300) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `staff_message_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_computers_club_zone_id_number_unique` (`club_zone_id`,`number`),
  UNIQUE KEY `club_computers_club_id_id_unique` (`club_id`,`id`),
  UNIQUE KEY `club_computers_club_device_id_unique` (`club_id`,`device_id`),
  UNIQUE KEY `club_computers_agent_token_hash_unique` (`agent_token_hash`),
  UNIQUE KEY `club_computers_club_mac_unique` (`club_id`,`mac_address`),
  KEY `club_computers_status_index` (`status`),
  KEY `club_computers_last_seen_at_index` (`last_seen_at`),
  KEY `club_computers_club_id_index` (`club_id`),
  KEY `club_computers_club_zone_fk` (`club_id`,`club_zone_id`),
  KEY `club_computers_club_device_name_index` (`club_id`,`device_name`),
  KEY `club_computers_current_session_user_id_foreign` (`current_session_user_id`),
  KEY `club_computers_staff_user_id_foreign` (`staff_user_id`),
  KEY `club_computers_club_id_wake_requested_at_index` (`club_id`,`wake_requested_at`),
  CONSTRAINT `club_computers_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `club_computers_club_zone_fk` FOREIGN KEY (`club_id`, `club_zone_id`) REFERENCES `club_zones` (`club_id`, `id`) ON DELETE CASCADE,
  CONSTRAINT `club_computers_current_session_user_id_foreign` FOREIGN KEY (`current_session_user_id`) REFERENCES `users` (`id`) ON DELETE SET NULL,
  CONSTRAINT `club_computers_staff_user_id_foreign` FOREIGN KEY (`staff_user_id`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_invitations` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `email` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `role` varchar(40) COLLATE utf8mb4_unicode_ci NOT NULL,
  `token_hash` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `invited_by` bigint unsigned DEFAULT NULL,
  `expires_at` timestamp NOT NULL,
  `accepted_at` timestamp NULL DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_invitations_token_hash_unique` (`token_hash`),
  KEY `club_invitations_invited_by_foreign` (`invited_by`),
  KEY `club_invitations_club_id_accepted_at_expires_at_index` (`club_id`,`accepted_at`,`expires_at`),
  CONSTRAINT `club_invitations_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `club_invitations_invited_by_foreign` FOREIGN KEY (`invited_by`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_load_snapshots` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `captured_at` timestamp NOT NULL,
  `weekday` tinyint unsigned NOT NULL,
  `hour` tinyint unsigned NOT NULL,
  `computers` int unsigned NOT NULL,
  `online` int unsigned NOT NULL,
  `busy` int unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `club_load_snapshots_club_id_captured_at_index` (`club_id`,`captured_at`),
  KEY `club_load_snapshots_captured_at_index` (`captured_at`),
  CONSTRAINT `club_load_snapshots_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_memberships` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `role` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'Пользователь',
  `permissions` json DEFAULT NULL,
  `discount` decimal(5,2) NOT NULL DEFAULT '0.00',
  `total_spent` decimal(12,2) NOT NULL DEFAULT '0.00',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `notifications_seen_at` timestamp NULL DEFAULT NULL,
  `can_manage_integrations` tinyint(1) NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_memberships_club_id_user_id_unique` (`club_id`,`user_id`),
  KEY `club_memberships_user_id_foreign` (`user_id`),
  CONSTRAINT `club_memberships_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `club_memberships_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_request_metrics` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `minute` int unsigned NOT NULL,
  `agent_requests` int unsigned NOT NULL DEFAULT '0',
  `api_requests` int unsigned NOT NULL DEFAULT '0',
  `panel_requests` int unsigned NOT NULL DEFAULT '0',
  `guest_requests` int unsigned NOT NULL DEFAULT '0',
  `errors` int unsigned NOT NULL DEFAULT '0',
  `duration_ms` bigint unsigned NOT NULL DEFAULT '0',
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_request_metrics_club_id_minute_unique` (`club_id`,`minute`),
  KEY `club_request_metrics_minute_index` (`minute`),
  CONSTRAINT `club_request_metrics_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_services` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `name` varchar(150) COLLATE utf8mb4_unicode_ci NOT NULL,
  `price` decimal(12,2) NOT NULL,
  `unit` varchar(30) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `is_active` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `club_services_club_id_is_active_name_index` (`club_id`,`is_active`,`name`),
  CONSTRAINT `club_services_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_subscriptions` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `saas_plan_id` bigint unsigned NOT NULL,
  `status` varchar(30) COLLATE utf8mb4_unicode_ci NOT NULL,
  `started_at` timestamp NOT NULL,
  `trial_ends_at` timestamp NULL DEFAULT NULL,
  `current_period_ends_at` timestamp NULL DEFAULT NULL,
  `canceled_at` timestamp NULL DEFAULT NULL,
  `provider` varchar(40) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `provider_reference` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_subscriptions_club_id_unique` (`club_id`),
  KEY `club_subscriptions_saas_plan_id_foreign` (`saas_plan_id`),
  KEY `club_subscriptions_status_index` (`status`),
  CONSTRAINT `club_subscriptions_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `club_subscriptions_saas_plan_id_foreign` FOREIGN KEY (`saas_plan_id`) REFERENCES `saas_plans` (`id`) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_zone_tariff` (
  `club_zone_id` bigint unsigned NOT NULL,
  `tariff_id` bigint unsigned NOT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`club_zone_id`,`tariff_id`),
  KEY `club_zone_tariff_tariff_id_foreign` (`tariff_id`),
  KEY `club_zone_tariff_club_id_index` (`club_id`),
  KEY `club_zone_tariff_club_zone_fk` (`club_id`,`club_zone_id`),
  KEY `club_zone_tariff_tariff_fk` (`club_id`,`tariff_id`),
  CONSTRAINT `club_zone_tariff_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `club_zone_tariff_club_zone_fk` FOREIGN KEY (`club_id`, `club_zone_id`) REFERENCES `club_zones` (`club_id`, `id`) ON DELETE CASCADE,
  CONSTRAINT `club_zone_tariff_tariff_fk` FOREIGN KEY (`club_id`, `tariff_id`) REFERENCES `tariffs` (`club_id`, `id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `club_zones` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `capacity` int unsigned DEFAULT NULL,
  `price_per_hour` decimal(12,2) DEFAULT NULL,
  `icon` varchar(40) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'desktop',
  `theme` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'neutral',
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'available',
  `is_featured` tinyint(1) NOT NULL DEFAULT '0',
  `sort_order` int unsigned NOT NULL DEFAULT '0',
  `position_x` int unsigned NOT NULL DEFAULT '20',
  `position_y` int unsigned NOT NULL DEFAULT '20',
  `width` int unsigned NOT NULL DEFAULT '320',
  `height` int unsigned NOT NULL DEFAULT '180',
  `rotation` tinyint unsigned NOT NULL DEFAULT '0',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `club_zones_club_id_id_unique` (`club_id`,`id`),
  KEY `club_zones_status_index` (`status`),
  KEY `club_zones_sort_order_index` (`sort_order`),
  KEY `club_zones_club_id_index` (`club_id`),
  CONSTRAINT `club_zones_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `clubs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `slug` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `settings` json DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `installation_key_hash` char(64) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `installation_key_prefix` char(12) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `installation_key` text COLLATE utf8mb4_unicode_ci,
  `locale` varchar(16) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `timezone` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'Asia/Almaty',
  `agent_exit_pin_hash` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `agent_exit_pin_changed_at` timestamp NULL DEFAULT NULL,
  `agent_staff_minutes` smallint unsigned NOT NULL DEFAULT '15',
  `logo_path` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `signin_image_path` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `clubs_slug_unique` (`slug`),
  UNIQUE KEY `clubs_installation_key_hash_unique` (`installation_key_hash`),
  KEY `clubs_installation_key_prefix_index` (`installation_key_prefix`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `computer_reservations` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `club_computer_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned DEFAULT NULL,
  `customer_name` varchar(120) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `customer_phone` varchar(20) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `note` varchar(500) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `starts_at` datetime NOT NULL,
  `ends_at` datetime NOT NULL,
  `arrived_at` datetime DEFAULT NULL,
  `cancelled_at` datetime DEFAULT NULL,
  `created_by` bigint unsigned DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `computer_reservations_user_id_foreign` (`user_id`),
  KEY `computer_reservations_created_by_foreign` (`created_by`),
  KEY `computer_reservations_club_id_starts_at_index` (`club_id`,`starts_at`),
  KEY `computer_reservations_club_computer_id_starts_at_index` (`club_computer_id`,`starts_at`),
  KEY `computer_reservations_club_computer_fk` (`club_id`,`club_computer_id`),
  CONSTRAINT `computer_reservations_club_computer_fk` FOREIGN KEY (`club_id`, `club_computer_id`) REFERENCES `club_computers` (`club_id`, `id`) ON DELETE CASCADE,
  CONSTRAINT `computer_reservations_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `computer_reservations_created_by_foreign` FOREIGN KEY (`created_by`) REFERENCES `users` (`id`) ON DELETE SET NULL,
  CONSTRAINT `computer_reservations_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `failed_jobs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `connection` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `queue` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payload` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `exception` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `failed_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `failed_jobs_uuid_unique` (`uuid`),
  KEY `failed_jobs_connection_queue_failed_at_index` (`connection`,`queue`,`failed_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `friendships` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `requester_id` bigint unsigned NOT NULL,
  `addressee_id` bigint unsigned NOT NULL,
  `accepted_at` datetime DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `friendships_club_id_requester_id_addressee_id_unique` (`club_id`,`requester_id`,`addressee_id`),
  KEY `friendships_requester_id_foreign` (`requester_id`),
  KEY `friendships_addressee_id_foreign` (`addressee_id`),
  KEY `friendships_club_id_addressee_id_index` (`club_id`,`addressee_id`),
  CONSTRAINT `friendships_addressee_id_foreign` FOREIGN KEY (`addressee_id`) REFERENCES `users` (`id`) ON DELETE CASCADE,
  CONSTRAINT `friendships_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `friendships_requester_id_foreign` FOREIGN KEY (`requester_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `job_batches` (
  `id` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `total_jobs` int NOT NULL,
  `pending_jobs` int NOT NULL,
  `failed_jobs` int NOT NULL,
  `failed_job_ids` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `options` mediumtext COLLATE utf8mb4_unicode_ci,
  `cancelled_at` int DEFAULT NULL,
  `created_at` int NOT NULL,
  `finished_at` int DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `jobs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `queue` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payload` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `attempts` smallint unsigned NOT NULL,
  `reserved_at` int unsigned DEFAULT NULL,
  `available_at` int unsigned NOT NULL,
  `created_at` int unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `jobs_queue_index` (`queue`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `languages` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `code` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `is_enabled` tinyint(1) NOT NULL DEFAULT '1',
  `is_system` tinyint(1) NOT NULL DEFAULT '0',
  `sort_order` smallint unsigned NOT NULL DEFAULT '0',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `languages_code_unique` (`code`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `member_balances` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `balance` decimal(12,2) NOT NULL DEFAULT '0.00',
  `bonus_balance` decimal(12,2) NOT NULL DEFAULT '0.00',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `member_balances_club_id_user_id_unique` (`club_id`,`user_id`),
  CONSTRAINT `member_balances_membership_fk` FOREIGN KEY (`club_id`, `user_id`) REFERENCES `club_memberships` (`club_id`, `user_id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `migrations` (
  `id` int unsigned NOT NULL AUTO_INCREMENT,
  `migration` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `batch` int NOT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `news` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned DEFAULT NULL,
  `title` varchar(150) COLLATE utf8mb4_unicode_ci NOT NULL,
  `body` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `image_path` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `is_published` tinyint(1) NOT NULL DEFAULT '1',
  `created_by` bigint unsigned DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `news_created_by_foreign` (`created_by`),
  KEY `news_club_id_is_published_created_at_index` (`club_id`,`is_published`,`created_at`),
  CONSTRAINT `news_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `news_created_by_foreign` FOREIGN KEY (`created_by`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `password_recovery_codes` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `code_hash` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `attempts` tinyint unsigned NOT NULL DEFAULT '0',
  `expires_at` datetime NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `password_recovery_codes_user_id_created_at_index` (`user_id`,`created_at`),
  CONSTRAINT `password_recovery_codes_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `password_reset_tokens` (
  `email` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `token` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`email`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `personal_access_tokens` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `tokenable_type` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `tokenable_id` bigint unsigned NOT NULL,
  `name` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `token` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `abilities` text COLLATE utf8mb4_unicode_ci,
  `last_used_at` timestamp NULL DEFAULT NULL,
  `expires_at` timestamp NULL DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `personal_access_tokens_token_unique` (`token`),
  KEY `personal_access_tokens_tokenable_type_tokenable_id_index` (`tokenable_type`,`tokenable_id`),
  KEY `personal_access_tokens_expires_at_index` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `platform_logs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned DEFAULT NULL,
  `club_id` bigint unsigned DEFAULT NULL,
  `event` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` varchar(500) COLLATE utf8mb4_unicode_ci NOT NULL,
  `subject_type` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `subject_id` bigint unsigned DEFAULT NULL,
  `details` json DEFAULT NULL,
  `ip_address` varchar(45) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_agent` varchar(500) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `platform_logs_user_id_foreign` (`user_id`),
  KEY `platform_logs_subject_type_subject_id_index` (`subject_type`,`subject_id`),
  KEY `platform_logs_created_at_index` (`created_at`),
  KEY `platform_logs_club_id_created_at_index` (`club_id`,`created_at`),
  CONSTRAINT `platform_logs_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE SET NULL,
  CONSTRAINT `platform_logs_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `platform_settings` (
  `key` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `value` json DEFAULT NULL,
  `updated_by` bigint unsigned DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`key`),
  KEY `platform_settings_updated_by_foreign` (`updated_by`),
  CONSTRAINT `platform_settings_updated_by_foreign` FOREIGN KEY (`updated_by`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `product_categories` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `product_categories_club_id_name_unique` (`club_id`,`name`),
  UNIQUE KEY `product_categories_club_id_id_unique` (`club_id`,`id`),
  KEY `product_categories_club_id_index` (`club_id`),
  CONSTRAINT `product_categories_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `products` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `sku` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `category` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `product_category_id` bigint unsigned DEFAULT NULL,
  `price` decimal(12,2) NOT NULL,
  `quantity` int unsigned NOT NULL DEFAULT '0',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  `deleted_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `products_club_id_sku_unique` (`club_id`,`sku`),
  UNIQUE KEY `products_club_id_id_unique` (`club_id`,`id`),
  KEY `products_category_index` (`category`),
  KEY `products_product_category_id_foreign` (`product_category_id`),
  KEY `products_club_id_index` (`club_id`),
  KEY `products_club_category_fk` (`club_id`,`product_category_id`),
  KEY `products_club_id_name_index` (`club_id`,`name`),
  CONSTRAINT `products_club_category_fk` FOREIGN KEY (`club_id`, `product_category_id`) REFERENCES `product_categories` (`club_id`, `id`) ON DELETE RESTRICT,
  CONSTRAINT `products_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `request_metrics` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `minute` int unsigned NOT NULL,
  `channel` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL,
  `requests` int unsigned NOT NULL,
  `errors` int unsigned NOT NULL,
  `duration_ms` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `request_metrics_minute_channel_unique` (`minute`,`channel`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `saas_plans` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `slug` varchar(80) COLLATE utf8mb4_unicode_ci NOT NULL,
  `name` varchar(120) COLLATE utf8mb4_unicode_ci NOT NULL,
  `price_monthly` decimal(12,2) DEFAULT NULL,
  `trial_days` smallint unsigned NOT NULL DEFAULT '0',
  `features` json DEFAULT NULL,
  `is_active` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `saas_plans_slug_unique` (`slug`),
  KEY `saas_plans_is_active_index` (`is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `server_metrics` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `minute` int unsigned NOT NULL,
  `load_1` decimal(6,2) DEFAULT NULL,
  `load_5` decimal(6,2) DEFAULT NULL,
  `load_15` decimal(6,2) DEFAULT NULL,
  `cpu_count` smallint unsigned DEFAULT NULL,
  `memory_total_mb` int unsigned DEFAULT NULL,
  `memory_used_mb` int unsigned DEFAULT NULL,
  `disk_total_mb` bigint unsigned DEFAULT NULL,
  `disk_used_mb` bigint unsigned DEFAULT NULL,
  `uptime_seconds` int unsigned DEFAULT NULL,
  `database_mb` bigint unsigned DEFAULT NULL,
  `queue_pending` int unsigned NOT NULL DEFAULT '0',
  `queue_failed` int unsigned NOT NULL DEFAULT '0',
  `queue_oldest_seconds` int unsigned DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `server_metrics_minute_unique` (`minute`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `session_purchases` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `session_id` varchar(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `kind` varchar(10) COLLATE utf8mb4_unicode_ci NOT NULL,
  `tariff_id` bigint unsigned DEFAULT NULL,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `minutes` int unsigned NOT NULL,
  `main_cents` bigint unsigned NOT NULL DEFAULT '0',
  `bonus_cents` bigint unsigned NOT NULL DEFAULT '0',
  `expires_at` datetime DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `session_purchases_user_id_foreign` (`user_id`),
  KEY `session_purchases_tariff_id_foreign` (`tariff_id`),
  KEY `session_purchases_club_id_session_id_index` (`club_id`,`session_id`),
  KEY `session_purchases_created_at_index` (`created_at`),
  CONSTRAINT `session_purchases_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `session_purchases_tariff_id_foreign` FOREIGN KEY (`tariff_id`) REFERENCES `tariffs` (`id`) ON DELETE SET NULL,
  CONSTRAINT `session_purchases_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `sessions` (
  `id` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `user_id` bigint unsigned DEFAULT NULL,
  `ip_address` varchar(45) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `user_agent` text COLLATE utf8mb4_unicode_ci,
  `payload` longtext COLLATE utf8mb4_unicode_ci NOT NULL,
  `last_activity` int NOT NULL,
  PRIMARY KEY (`id`),
  KEY `sessions_user_id_index` (`user_id`),
  KEY `sessions_last_activity_index` (`last_activity`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `shop_orders` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `club_computer_id` bigint unsigned DEFAULT NULL,
  `computer_name` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `items` json NOT NULL,
  `amount` decimal(12,2) NOT NULL,
  `status` varchar(20) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'new',
  `handled_by` bigint unsigned DEFAULT NULL,
  `handled_at` datetime DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `shop_orders_user_id_foreign` (`user_id`),
  KEY `shop_orders_club_computer_id_foreign` (`club_computer_id`),
  KEY `shop_orders_handled_by_foreign` (`handled_by`),
  KEY `shop_orders_club_id_status_id_index` (`club_id`,`status`,`id`),
  KEY `shop_orders_club_id_id_index` (`club_id`,`id`),
  CONSTRAINT `shop_orders_club_computer_id_foreign` FOREIGN KEY (`club_computer_id`) REFERENCES `club_computers` (`id`) ON DELETE SET NULL,
  CONSTRAINT `shop_orders_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `shop_orders_handled_by_foreign` FOREIGN KEY (`handled_by`) REFERENCES `users` (`id`) ON DELETE SET NULL,
  CONSTRAINT `shop_orders_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `stock_movements` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `product_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned DEFAULT NULL,
  `type` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `quantity` int unsigned NOT NULL,
  `balance_after` int unsigned NOT NULL,
  `note` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`),
  KEY `stock_movements_user_id_foreign` (`user_id`),
  KEY `stock_movements_product_id_created_at_index` (`product_id`,`created_at`),
  KEY `stock_movements_type_index` (`type`),
  KEY `stock_movements_club_id_index` (`club_id`),
  KEY `stock_movements_club_product_fk` (`club_id`,`product_id`),
  KEY `stock_movements_club_user_fk` (`club_id`,`user_id`),
  KEY `stock_movements_club_id_created_at_index` (`club_id`,`created_at`),
  CONSTRAINT `stock_movements_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `stock_movements_club_product_fk` FOREIGN KEY (`club_id`, `product_id`) REFERENCES `products` (`club_id`, `id`) ON DELETE CASCADE,
  CONSTRAINT `stock_movements_club_user_fk` FOREIGN KEY (`club_id`, `user_id`) REFERENCES `club_memberships` (`club_id`, `user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `tariffs` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(100) COLLATE utf8mb4_unicode_ci NOT NULL,
  `description` text COLLATE utf8mb4_unicode_ci,
  `duration_minutes` int unsigned NOT NULL,
  `price` decimal(12,2) NOT NULL,
  `is_active` tinyint(1) NOT NULL DEFAULT '1',
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `payment_method` varchar(32) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'main_balance_only',
  `currency` varchar(10) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'KZT',
  `discount_enabled` tinyint(1) NOT NULL DEFAULT '0',
  `user_roles` json DEFAULT NULL,
  `usage_limit_enabled` tinyint(1) NOT NULL DEFAULT '0',
  `usage_limit_minutes` int unsigned DEFAULT NULL,
  `quantity_limit_enabled` tinyint(1) NOT NULL DEFAULT '0',
  `quantity_limit` int unsigned DEFAULT NULL,
  `validity_periods` json DEFAULT NULL,
  `sale_periods` json DEFAULT NULL,
  `club_id` bigint unsigned NOT NULL,
  `deleted_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `tariffs_club_id_name_unique` (`club_id`,`name`),
  UNIQUE KEY `tariffs_club_id_id_unique` (`club_id`,`id`),
  KEY `tariffs_duration_minutes_index` (`duration_minutes`),
  KEY `tariffs_is_active_index` (`is_active`),
  KEY `tariffs_club_id_index` (`club_id`),
  CONSTRAINT `tariffs_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `time_packages` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL,
  `tariff_id` bigint unsigned DEFAULT NULL,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `minutes_left` int unsigned NOT NULL,
  `expires_at` datetime NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `time_packages_user_id_foreign` (`user_id`),
  KEY `time_packages_tariff_id_foreign` (`tariff_id`),
  KEY `time_packages_club_id_user_id_expires_at_index` (`club_id`,`user_id`,`expires_at`),
  CONSTRAINT `time_packages_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `time_packages_tariff_id_foreign` FOREIGN KEY (`tariff_id`) REFERENCES `tariffs` (`id`) ON DELETE SET NULL,
  CONSTRAINT `time_packages_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `todo_tasks` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `title` varchar(200) COLLATE utf8mb4_unicode_ci NOT NULL,
  `due_on` date NOT NULL,
  `created_by` bigint unsigned DEFAULT NULL,
  `done_at` datetime DEFAULT NULL,
  `done_by` bigint unsigned DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `todo_tasks_created_by_foreign` (`created_by`),
  KEY `todo_tasks_done_by_foreign` (`done_by`),
  KEY `todo_tasks_club_id_due_on_index` (`club_id`,`due_on`),
  CONSTRAINT `todo_tasks_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `todo_tasks_created_by_foreign` FOREIGN KEY (`created_by`) REFERENCES `users` (`id`) ON DELETE SET NULL,
  CONSTRAINT `todo_tasks_done_by_foreign` FOREIGN KEY (`done_by`) REFERENCES `users` (`id`) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `transfer_codes` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id` bigint unsigned NOT NULL,
  `friendship_id` bigint unsigned NOT NULL,
  `amount_cents` bigint unsigned NOT NULL,
  `code_hash` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `attempts` tinyint unsigned NOT NULL DEFAULT '0',
  `expires_at` datetime NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `transfer_codes_friendship_id_foreign` (`friendship_id`),
  KEY `transfer_codes_user_id_created_at_index` (`user_id`,`created_at`),
  CONSTRAINT `transfer_codes_friendship_id_foreign` FOREIGN KEY (`friendship_id`) REFERENCES `friendships` (`id`) ON DELETE CASCADE,
  CONSTRAINT `transfer_codes_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `translations` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `language_id` bigint unsigned NOT NULL,
  `group` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT '*',
  `key` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `key_hash` char(40) COLLATE utf8mb4_unicode_ci NOT NULL,
  `value` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `translations_language_id_group_key_hash_unique` (`language_id`,`group`,`key_hash`),
  CONSTRAINT `translations_language_id_foreign` FOREIGN KEY (`language_id`) REFERENCES `languages` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `users` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `phone` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `email` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `email_verified_at` timestamp NULL DEFAULT NULL,
  `password` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,
  `remember_token` varchar(100) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  `is_superadmin` tinyint(1) NOT NULL DEFAULT '0',
  `blocked_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `users_phone_unique` (`phone`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `webhook_deliveries` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `webhook_endpoint_id` bigint unsigned NOT NULL,
  `event_id` char(36) COLLATE utf8mb4_unicode_ci NOT NULL,
  `event` varchar(64) COLLATE utf8mb4_unicode_ci NOT NULL,
  `payload` json NOT NULL,
  `status` varchar(16) COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'pending',
  `attempts` smallint unsigned NOT NULL DEFAULT '0',
  `last_status_code` smallint unsigned DEFAULT NULL,
  `last_error` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL,
  `next_attempt_at` timestamp NULL DEFAULT NULL,
  `delivered_at` timestamp NULL DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `webhook_deliveries_event_id_unique` (`event_id`),
  KEY `webhook_deliveries_webhook_endpoint_id_created_at_index` (`webhook_endpoint_id`,`created_at`),
  KEY `webhook_deliveries_status_next_attempt_at_index` (`status`,`next_attempt_at`),
  KEY `webhook_deliveries_club_id_status_created_at_index` (`club_id`,`status`,`created_at`),
  CONSTRAINT `webhook_deliveries_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `webhook_deliveries_webhook_endpoint_id_foreign` FOREIGN KEY (`webhook_endpoint_id`) REFERENCES `webhook_endpoints` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `webhook_endpoints` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `club_id` bigint unsigned NOT NULL,
  `created_by` bigint unsigned NOT NULL,
  `url` varchar(500) COLLATE utf8mb4_unicode_ci NOT NULL,
  `secret` text COLLATE utf8mb4_unicode_ci NOT NULL,
  `events` json NOT NULL,
  `is_active` tinyint(1) NOT NULL DEFAULT '1',
  `failure_count` int unsigned NOT NULL DEFAULT '0',
  `last_success_at` timestamp NULL DEFAULT NULL,
  `last_failure_at` timestamp NULL DEFAULT NULL,
  `created_at` timestamp NULL DEFAULT NULL,
  `updated_at` timestamp NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `webhook_endpoints_created_by_foreign` (`created_by`),
  KEY `webhook_endpoints_club_id_is_active_index` (`club_id`,`is_active`),
  CONSTRAINT `webhook_endpoints_club_id_foreign` FOREIGN KEY (`club_id`) REFERENCES `clubs` (`id`) ON DELETE CASCADE,
  CONSTRAINT `webhook_endpoints_created_by_foreign` FOREIGN KEY (`created_by`) REFERENCES `users` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;

