-- Демо-данные tools/visual/fixtures/demo.json, записанные старым кодом (tools/visual/old/seed.php) в MySQL.
-- Сгенерировано tools/migrate-from-mysql/dump-schema.sh, не править руками.
/*!40103 SET @OLD_TIME_ZONE=@@TIME_ZONE */;
/*!40103 SET TIME_ZONE='+00:00' */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;
/*!40101 SET @OLD_SQL_MODE=@@SQL_MODE, SQL_MODE='NO_AUTO_VALUE_ON_ZERO' */;
/*!40111 SET @OLD_SQL_NOTES=@@SQL_NOTES, SQL_NOTES=0 */;

LOCK TABLES `activity_logs` WRITE;
/*!40000 ALTER TABLE `activity_logs` DISABLE KEYS */;
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (1,'cash.shift_opened','Открыта кассовая смена',4,'App\\Models\\CashShift',1,'{\"opening_balance\": 10000}','127.0.0.1','Symfony','2026-10-13 04:00:00','2026-10-13 04:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (2,'stock.written_off','Списание при продаже: Coca-Cola 0,5 л, 3 шт.',4,'App\\Models\\Product',1,'{\"note\": \"Продажа POS\", \"quantity\": 3, \"balance_after\": 45}','127.0.0.1','Symfony','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (3,'stock.written_off','Списание при продаже: Lay\'s сметана и лук, 2 шт.',4,'App\\Models\\Product',4,'{\"note\": \"Продажа POS\", \"quantity\": 2, \"balance_after\": 28}','127.0.0.1','Symfony','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (4,'product.purchased','Покупка товаров: Coca-Cola 0,5 л × 3, Lay\'s сметана и лук × 2',4,'App\\Models\\CashTransaction',1,'{\"amount\": 2550, \"item_count\": 2}','127.0.0.1','Symfony','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (5,'cash.income','Пополнение основной баланс: Асель Нурланова (карта)',4,'App\\Models\\CashTransaction',2,'{\"amount\": 20000, \"new_balance\": 35750.5, \"balance_type\": \"основной баланс\", \"payment_method\": \"карта\"}','127.0.0.1','Symfony','2026-10-13 10:00:00','2026-10-13 10:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (6,'user.balance_topped_up','Пополнение основной баланс: Асель Нурланова (карта)',4,'App\\Models\\User',9,'{\"amount\": 20000, \"new_balance\": 35750.5, \"balance_type\": \"основной баланс\", \"payment_method\": \"карта\"}','127.0.0.1','Symfony','2026-10-13 10:00:00','2026-10-13 10:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (7,'cash.expense','Расход из кассы: Закупка напитков',4,'App\\Models\\CashTransaction',3,'{\"amount\": 3500}','127.0.0.1','Symfony','2026-10-13 13:30:00','2026-10-13 13:30:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (8,'cash.shift_closed','Закрыта кассовая смена',4,'App\\Models\\CashShift',1,'{\"closing_balance\": 29050}','127.0.0.1','Symfony','2026-10-13 18:00:00','2026-10-13 18:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (9,'cash.shift_opened','Открыта кассовая смена',4,'App\\Models\\CashShift',2,'{\"opening_balance\": 15000}','127.0.0.1','Symfony','2026-10-14 04:00:00','2026-10-14 04:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (10,'stock.written_off','Списание при продаже: Пицца пепперони, 2 шт.',4,'App\\Models\\Product',6,'{\"note\": \"Продажа POS\", \"quantity\": 2, \"balance_after\": 8}','127.0.0.1','Symfony','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (11,'stock.written_off','Списание при продаже: Red Bull 0,25 л, 2 шт.',4,'App\\Models\\Product',2,'{\"note\": \"Продажа POS\", \"quantity\": 2, \"balance_after\": 22}','127.0.0.1','Symfony','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (12,'product.purchased','Покупка товаров: Пицца пепперони × 2, Red Bull 0,25 л × 2',4,'App\\Models\\CashTransaction',4,'{\"amount\": 6200, \"item_count\": 2}','127.0.0.1','Symfony','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (13,'cash.income','Пополнение основной баланс: Арман Бекетов (наличные)',4,'App\\Models\\CashTransaction',5,'{\"amount\": 5000, \"new_balance\": 11400, \"balance_type\": \"основной баланс\", \"payment_method\": \"наличные\"}','127.0.0.1','Symfony','2026-10-14 11:45:00','2026-10-14 11:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (14,'user.balance_topped_up','Пополнение основной баланс: Арман Бекетов (наличные)',4,'App\\Models\\User',12,'{\"amount\": 5000, \"new_balance\": 11400, \"balance_type\": \"основной баланс\", \"payment_method\": \"наличные\"}','127.0.0.1','Symfony','2026-10-14 11:45:00','2026-10-14 11:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (15,'stock.written_off','Списание при продаже: Хот-дог, 3 шт.',4,'App\\Models\\Product',7,'{\"note\": \"Продажа POS\", \"quantity\": 3, \"balance_after\": 12}','127.0.0.1','Symfony','2026-10-14 15:15:00','2026-10-14 15:15:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (16,'product.purchased','Покупка товаров: Хот-дог × 3, Печать А4 × 12',4,'App\\Models\\CashTransaction',6,'{\"amount\": 3300, \"item_count\": 2}','127.0.0.1','Symfony','2026-10-14 15:15:00','2026-10-14 15:15:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (17,'cash.shift_closed','Закрыта кассовая смена',4,'App\\Models\\CashShift',2,'{\"closing_balance\": 29500}','127.0.0.1','Symfony','2026-10-14 18:30:00','2026-10-14 18:30:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (18,'cash.shift_opened','Открыта кассовая смена',4,'App\\Models\\CashShift',3,'{\"opening_balance\": 20000}','127.0.0.1','Symfony','2026-10-15 04:00:00','2026-10-15 04:00:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (19,'cash.income','Пополнение основной баланс: Нурлан Ахметов (наличные)',4,'App\\Models\\CashTransaction',7,'{\"amount\": 5000, \"new_balance\": 8500, \"balance_type\": \"основной баланс\", \"payment_method\": \"наличные\"}','127.0.0.1','Symfony','2026-10-15 04:15:00','2026-10-15 04:15:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (20,'user.balance_topped_up','Пополнение основной баланс: Нурлан Ахметов (наличные)',4,'App\\Models\\User',6,'{\"amount\": 5000, \"new_balance\": 8500, \"balance_type\": \"основной баланс\", \"payment_method\": \"наличные\"}','127.0.0.1','Symfony','2026-10-15 04:15:00','2026-10-15 04:15:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (21,'stock.written_off','Списание при продаже: Coca-Cola 0,5 л, 2 шт.',4,'App\\Models\\Product',1,'{\"note\": \"Продажа POS\", \"quantity\": 2, \"balance_after\": 43}','127.0.0.1','Symfony','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (22,'stock.written_off','Списание при продаже: Lay\'s сметана и лук, 1 шт.',4,'App\\Models\\Product',4,'{\"note\": \"Продажа POS\", \"quantity\": 1, \"balance_after\": 27}','127.0.0.1','Symfony','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (23,'product.purchased','Покупка товаров: Coca-Cola 0,5 л × 2, Lay\'s сметана и лук × 1',4,'App\\Models\\CashTransaction',8,'{\"amount\": 1500, \"item_count\": 2}','127.0.0.1','Symfony','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (24,'cash.income','Пополнение основной баланс: Мария Иванова (карта)',4,'App\\Models\\CashTransaction',9,'{\"amount\": 10000, \"new_balance\": 11200, \"balance_type\": \"основной баланс\", \"payment_method\": \"карта\"}','127.0.0.1','Symfony','2026-10-15 05:05:00','2026-10-15 05:05:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (25,'user.balance_topped_up','Пополнение основной баланс: Мария Иванова (карта)',4,'App\\Models\\User',7,'{\"amount\": 10000, \"new_balance\": 11200, \"balance_type\": \"основной баланс\", \"payment_method\": \"карта\"}','127.0.0.1','Symfony','2026-10-15 05:05:00','2026-10-15 05:05:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (26,'stock.written_off','Списание при продаже: Red Bull 0,25 л, 1 шт.',4,'App\\Models\\Product',2,'{\"note\": \"Продажа POS\", \"quantity\": 1, \"balance_after\": 21}','127.0.0.1','Symfony','2026-10-15 05:30:00','2026-10-15 05:30:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (27,'product.purchased','Покупка товаров: Red Bull 0,25 л × 1, Печать А4 × 10',4,'App\\Models\\CashTransaction',10,'{\"amount\": 1400, \"item_count\": 2}','127.0.0.1','Symfony','2026-10-15 05:30:00','2026-10-15 05:30:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (28,'cash.income','Пополнение бонусный баланс: Тимур Сапаров (QR-код)',4,'App\\Models\\CashTransaction',11,'{\"amount\": 2000, \"new_balance\": 3000, \"balance_type\": \"бонусный баланс\", \"payment_method\": \"QR-код\"}','127.0.0.1','Symfony','2026-10-15 06:10:00','2026-10-15 06:10:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (29,'user.balance_topped_up','Пополнение бонусный баланс: Тимур Сапаров (QR-код)',4,'App\\Models\\User',8,'{\"amount\": 2000, \"new_balance\": 3000, \"balance_type\": \"бонусный баланс\", \"payment_method\": \"QR-код\"}','127.0.0.1','Symfony','2026-10-15 06:10:00','2026-10-15 06:10:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (30,'cash.expense','Расход из кассы: Закупка воды',4,'App\\Models\\CashTransaction',12,'{\"amount\": 1500}','127.0.0.1','Symfony','2026-10-15 06:30:00','2026-10-15 06:30:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (31,'stock.written_off','Списание при продаже: Пицца пепперони, 1 шт.',4,'App\\Models\\Product',6,'{\"note\": \"Продажа POS\", \"quantity\": 1, \"balance_after\": 7}','127.0.0.1','Symfony','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (32,'stock.written_off','Списание при продаже: Хот-дог, 2 шт.',4,'App\\Models\\Product',7,'{\"note\": \"Продажа POS\", \"quantity\": 2, \"balance_after\": 10}','127.0.0.1','Symfony','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (33,'stock.written_off','Списание при продаже: Snickers, 1 шт.',4,'App\\Models\\Product',5,'{\"note\": \"Продажа POS\", \"quantity\": 1, \"balance_after\": 3}','127.0.0.1','Symfony','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (34,'product.purchased','Покупка товаров: Пицца пепперони × 1, Хот-дог × 2, Snickers × 1',4,'App\\Models\\CashTransaction',13,'{\"amount\": 4400, \"item_count\": 3}','127.0.0.1','Symfony','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (35,'auth.login','Вход в систему: Айдар Сейтказы',2,'App\\Models\\User',2,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 03:45:00','2026-10-15 03:45:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (36,'auth.login','Вход в систему: Ержан Касымов',4,'App\\Models\\User',4,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 03:58:00','2026-10-15 03:58:00',1);
INSERT INTO `activity_logs` (`id`, `event`, `description`, `user_id`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`, `club_id`) VALUES (37,'auth.login','Вход в систему: Дана Ермекова',5,'App\\Models\\User',5,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 04:05:00','2026-10-15 04:05:00',1);
/*!40000 ALTER TABLE `activity_logs` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `agent_qr_logins` WRITE;
/*!40000 ALTER TABLE `agent_qr_logins` DISABLE KEYS */;
INSERT INTO `agent_qr_logins` (`id`, `club_id`, `club_computer_id`, `code_hash`, `approved_user_id`, `approved_at`, `consumed_at`, `expires_at`, `created_at`, `updated_at`) VALUES ('5f0c7d1e-3a8b-4c2d-9e6f-0a1b2c3d4e5f',1,1,'881ebf2adec4a0630dd55205e28e40d732d9fc873a65bd7d3844dfff31abc6d8',NULL,NULL,NULL,'2026-10-15 07:04:00','2026-10-15 06:59:00','2026-10-15 06:59:00');
/*!40000 ALTER TABLE `agent_qr_logins` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `api_idempotency_keys` WRITE;
/*!40000 ALTER TABLE `api_idempotency_keys` DISABLE KEYS */;
/*!40000 ALTER TABLE `api_idempotency_keys` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `api_keys` WRITE;
/*!40000 ALTER TABLE `api_keys` DISABLE KEYS */;
/*!40000 ALTER TABLE `api_keys` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `api_request_logs` WRITE;
/*!40000 ALTER TABLE `api_request_logs` DISABLE KEYS */;
/*!40000 ALTER TABLE `api_request_logs` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `cash_shifts` WRITE;
/*!40000 ALTER TABLE `cash_shifts` DISABLE KEYS */;
INSERT INTO `cash_shifts` (`id`, `opened_by`, `closed_by`, `status`, `open_marker`, `opening_balance`, `closing_balance`, `opened_at`, `closed_at`, `created_at`, `updated_at`, `club_id`) VALUES (1,4,4,'closed',NULL,10000.00,29050.00,'2026-10-13 04:00:00','2026-10-13 18:00:00','2026-10-13 04:00:00','2026-10-13 18:00:00',1);
INSERT INTO `cash_shifts` (`id`, `opened_by`, `closed_by`, `status`, `open_marker`, `opening_balance`, `closing_balance`, `opened_at`, `closed_at`, `created_at`, `updated_at`, `club_id`) VALUES (2,4,4,'closed',NULL,15000.00,29500.00,'2026-10-14 04:00:00','2026-10-14 18:30:00','2026-10-14 04:00:00','2026-10-14 18:30:00',1);
INSERT INTO `cash_shifts` (`id`, `opened_by`, `closed_by`, `status`, `open_marker`, `opening_balance`, `closing_balance`, `opened_at`, `closed_at`, `created_at`, `updated_at`, `club_id`) VALUES (3,4,NULL,'open',1,20000.00,NULL,'2026-10-15 04:00:00',NULL,'2026-10-15 04:00:00','2026-10-15 04:00:00',1);
/*!40000 ALTER TABLE `cash_shifts` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `cash_transactions` WRITE;
/*!40000 ALTER TABLE `cash_transactions` DISABLE KEYS */;
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (1,1,NULL,4,'income',2550.00,'Продажа POS','{\"items\": [{\"name\": \"Coca-Cola 0,5 л\", \"price\": 450, \"total\": 1350, \"quantity\": 3, \"product_id\": 1}, {\"name\": \"Lay\'s сметана и лук\", \"price\": 600, \"total\": 1200, \"quantity\": 2, \"product_id\": 4}]}','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (2,1,NULL,4,'income',20000.00,'Пополнение основной баланс: Асель Нурланова (карта)','{\"top_up\": {\"bonus\": 0, \"user_id\": 9, \"balance_type\": \"balance\"}}','2026-10-13 10:00:00','2026-10-13 10:00:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (3,1,NULL,4,'expense',3500.00,'Закупка напитков',NULL,'2026-10-13 13:30:00','2026-10-13 13:30:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (4,2,NULL,4,'income',6200.00,'Продажа POS','{\"items\": [{\"name\": \"Пицца пепперони\", \"price\": 2200, \"total\": 4400, \"quantity\": 2, \"product_id\": 6}, {\"name\": \"Red Bull 0,25 л\", \"price\": 900, \"total\": 1800, \"quantity\": 2, \"product_id\": 2}]}','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (5,2,NULL,4,'income',5000.00,'Пополнение основной баланс: Арман Бекетов (наличные)','{\"top_up\": {\"bonus\": 0, \"user_id\": 12, \"balance_type\": \"balance\"}}','2026-10-14 11:45:00','2026-10-14 11:45:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (6,2,NULL,4,'income',3300.00,'Продажа POS','{\"items\": [{\"name\": \"Хот-дог\", \"price\": 900, \"total\": 2700, \"quantity\": 3, \"product_id\": 7}, {\"name\": \"Печать А4\", \"price\": 50, \"total\": 600, \"quantity\": 12, \"service_id\": 1}]}','2026-10-14 15:15:00','2026-10-14 15:15:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (7,3,NULL,4,'income',5000.00,'Пополнение основной баланс: Нурлан Ахметов (наличные)','{\"top_up\": {\"bonus\": 0, \"user_id\": 6, \"balance_type\": \"balance\"}}','2026-10-15 04:15:00','2026-10-15 04:15:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (8,3,NULL,4,'income',1500.00,'Продажа POS','{\"items\": [{\"name\": \"Coca-Cola 0,5 л\", \"price\": 450, \"total\": 900, \"quantity\": 2, \"product_id\": 1}, {\"name\": \"Lay\'s сметана и лук\", \"price\": 600, \"total\": 600, \"quantity\": 1, \"product_id\": 4}]}','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (9,3,NULL,4,'income',10000.00,'Пополнение основной баланс: Мария Иванова (карта)','{\"top_up\": {\"bonus\": 0, \"user_id\": 7, \"balance_type\": \"balance\"}}','2026-10-15 05:05:00','2026-10-15 05:05:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (10,3,NULL,4,'income',1400.00,'Продажа POS','{\"items\": [{\"name\": \"Red Bull 0,25 л\", \"price\": 900, \"total\": 900, \"quantity\": 1, \"product_id\": 2}, {\"name\": \"Печать А4\", \"price\": 50, \"total\": 500, \"quantity\": 10, \"service_id\": 1}]}','2026-10-15 05:30:00','2026-10-15 05:30:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (11,3,NULL,4,'income',2000.00,'Пополнение бонусный баланс: Тимур Сапаров (QR-код)','{\"top_up\": {\"bonus\": 0, \"user_id\": 8, \"balance_type\": \"bonus_balance\"}}','2026-10-15 06:10:00','2026-10-15 06:10:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (12,3,NULL,4,'expense',1500.00,'Закупка воды',NULL,'2026-10-15 06:30:00','2026-10-15 06:30:00',1);
INSERT INTO `cash_transactions` (`id`, `cash_shift_id`, `refund_of_id`, `user_id`, `type`, `amount`, `description`, `receipt_data`, `created_at`, `updated_at`, `club_id`) VALUES (13,3,NULL,4,'income',4400.00,'Продажа POS','{\"items\": [{\"name\": \"Пицца пепперони\", \"price\": 2200, \"total\": 2200, \"quantity\": 1, \"product_id\": 6}, {\"name\": \"Хот-дог\", \"price\": 900, \"total\": 1800, \"quantity\": 2, \"product_id\": 7}, {\"name\": \"Snickers\", \"price\": 400, \"total\": 400, \"quantity\": 1, \"product_id\": 5}]}','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
/*!40000 ALTER TABLE `cash_transactions` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `chat_messages` WRITE;
/*!40000 ALTER TABLE `chat_messages` DISABLE KEYS */;
INSERT INTO `chat_messages` (`id`, `club_id`, `customer_id`, `author_id`, `from_staff`, `body`, `club_computer_id`, `read_at`, `created_at`, `updated_at`) VALUES (1,1,6,6,0,'Здравствуйте! Можно ещё одну пачку чипсов?',1,'2026-10-15 06:41:00','2026-10-15 06:40:00','2026-10-15 06:40:00');
INSERT INTO `chat_messages` (`id`, `club_id`, `customer_id`, `author_id`, `from_staff`, `body`, `club_computer_id`, `read_at`, `created_at`, `updated_at`) VALUES (2,1,6,4,1,'Конечно, закажите в магазине на экране — принесём.',1,'2026-10-15 06:43:00','2026-10-15 06:42:00','2026-10-15 06:42:00');
INSERT INTO `chat_messages` (`id`, `club_id`, `customer_id`, `author_id`, `from_staff`, `body`, `club_computer_id`, `read_at`, `created_at`, `updated_at`) VALUES (3,1,9,9,0,'Мышь двоит клик на PC-05',5,NULL,'2026-10-15 06:55:00','2026-10-15 06:55:00');
INSERT INTO `chat_messages` (`id`, `club_id`, `customer_id`, `author_id`, `from_staff`, `body`, `club_computer_id`, `read_at`, `created_at`, `updated_at`) VALUES (4,1,9,9,0,'Можно пересесть?',5,NULL,'2026-10-15 06:56:00','2026-10-15 06:56:00');
/*!40000 ALTER TABLE `chat_messages` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_computers` WRITE;
/*!40000 ALTER TABLE `club_computers` DISABLE KEYS */;
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (1,1,1,'online',NULL,NULL,NULL,0,5,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:40',1,'VIP-01',NULL,NULL,'in_session','2.4.1',NULL,0,NULL,'demo-session-1',6,0,'2026-10-15 08:30:00',NULL,NULL,NULL,'00:1A:2B:3C:4D:01','192.168.10.11',NULL,'AMD Ryzen 7 7800X3D',8,32768,'NVIDIA GeForce RTX 4080','Windows 11 Pro 24H2','2026-10-15 04:01:00',NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (2,1,2,'online',NULL,NULL,NULL,0,30,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:50',1,'VIP-02',NULL,NULL,'idle','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,'00:1A:2B:3C:4D:02','192.168.10.12',NULL,'AMD Ryzen 7 7800X3D',8,32768,'NVIDIA GeForce RTX 4080','Windows 11 Pro 24H2','2026-10-15 04:02:00',NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (3,1,3,'online',NULL,NULL,NULL,1,55,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:30',1,'VIP-03',NULL,NULL,'locked','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (4,1,4,'offline',NULL,NULL,NULL,0,80,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-14 18:10:00',1,'VIP-04',NULL,NULL,'idle','2.3.0',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (5,2,5,'online',NULL,NULL,NULL,0,5,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:45',1,'PC-05',NULL,NULL,'in_session','2.4.1',NULL,0,NULL,'demo-session-5',7,0,'2026-10-15 07:10:00',NULL,NULL,NULL,'00:1A:2B:3C:4D:05','192.168.10.21',NULL,'Intel Core i5-13400F',10,16384,'NVIDIA GeForce RTX 4060','Windows 11 Pro 24H2','2026-10-15 04:03:00',NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (6,2,6,'online',NULL,NULL,NULL,0,25,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:55',1,NULL,'Гостевой',NULL,'in_session','2.4.1',NULL,0,NULL,'demo-session-6',NULL,1,'2026-10-15 09:00:00',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (7,2,7,'online',NULL,NULL,NULL,0,45,5,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:35',1,'PC-07',NULL,NULL,'locked','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (8,2,8,'available',NULL,NULL,NULL,0,65,5,'2026-10-15 07:00:00','2026-10-15 07:00:00',NULL,1,NULL,NULL,NULL,NULL,NULL,NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (9,2,9,'online',NULL,NULL,NULL,0,5,50,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:58',1,'PC-09',NULL,NULL,'locked','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (10,2,10,'offline',NULL,NULL,NULL,0,25,50,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-12 15:00:00',1,'PC-10',NULL,NULL,'idle','2.1.7',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (11,3,11,'online',NULL,NULL,NULL,0,10,10,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-15 06:59:52',1,NULL,'PS5 №1',NULL,'idle','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
INSERT INTO `club_computers` (`id`, `club_zone_id`, `number`, `status`, `power_command`, `power_command_at`, `wake_requested_at`, `is_reserved`, `position_x`, `position_y`, `created_at`, `updated_at`, `last_seen_at`, `club_id`, `device_name`, `display_name`, `device_id`, `agent_state`, `agent_version`, `agent_update_version`, `agent_update_offers`, `agent_update_offered_at`, `current_session_id`, `current_session_user_id`, `session_guest`, `session_ends_at`, `staff_user_id`, `staff_kind`, `staff_until`, `mac_address`, `local_ip`, `remote_ip`, `cpu_model`, `cpu_cores`, `ram_total_mb`, `gpu_model`, `os_version`, `hardware_reported_at`, `agent_token_hash`, `agent_token_issued_at`, `staff_message`, `staff_message_at`) VALUES (12,3,12,'offline',NULL,NULL,NULL,0,45,10,'2026-10-15 07:00:00','2026-10-15 07:00:00','2026-10-13 13:00:00',1,NULL,'PS5 №2',NULL,'idle','2.4.1',NULL,0,NULL,NULL,NULL,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
/*!40000 ALTER TABLE `club_computers` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_invitations` WRITE;
/*!40000 ALTER TABLE `club_invitations` DISABLE KEYS */;
/*!40000 ALTER TABLE `club_invitations` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_load_snapshots` WRITE;
/*!40000 ALTER TABLE `club_load_snapshots` DISABLE KEYS */;
/*!40000 ALTER TABLE `club_load_snapshots` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_memberships` WRITE;
/*!40000 ALTER TABLE `club_memberships` DISABLE KEYS */;
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (1,1,2,'owner',NULL,0.00,0.00,'2026-09-01 05:00:00','2026-09-01 05:00:00',NULL,1);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (2,1,3,'admin',NULL,0.00,0.00,'2026-09-02 06:00:00','2026-09-02 06:00:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (3,1,4,'cashier',NULL,0.00,0.00,'2026-09-02 06:05:00','2026-09-02 06:05:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (4,1,5,'operator','{\"todos.delete\": true}',0.00,0.00,'2026-09-02 06:10:00','2026-09-02 06:10:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (5,1,6,'client',NULL,0.00,18400.00,'2026-09-10 13:20:00','2026-09-10 13:20:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (6,1,7,'client',NULL,5.00,5600.00,'2026-09-12 09:00:00','2026-09-12 09:00:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (7,1,8,'client',NULL,0.00,0.00,'2026-09-20 15:45:00','2026-09-20 15:45:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (8,1,9,'client',NULL,10.00,42000.00,'2026-09-21 11:30:00','2026-09-21 11:30:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (9,1,10,'client',NULL,0.00,900.00,'2026-10-01 07:00:00','2026-10-01 07:00:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (10,1,11,'client',NULL,0.00,0.00,'2026-10-05 14:15:00','2026-10-05 14:15:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (11,1,12,'client',NULL,0.00,12000.00,'2026-10-08 16:40:00','2026-10-08 16:40:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (12,1,13,'client',NULL,0.00,0.00,'2026-10-10 08:00:00','2026-10-10 08:00:00',NULL,0);
INSERT INTO `club_memberships` (`id`, `club_id`, `user_id`, `role`, `permissions`, `discount`, `total_spent`, `created_at`, `updated_at`, `notifications_seen_at`, `can_manage_integrations`) VALUES (13,2,14,'owner',NULL,0.00,0.00,'2026-08-01 05:00:00','2026-08-01 05:00:00',NULL,0);
/*!40000 ALTER TABLE `club_memberships` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_request_metrics` WRITE;
/*!40000 ALTER TABLE `club_request_metrics` DISABLE KEYS */;
/*!40000 ALTER TABLE `club_request_metrics` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_services` WRITE;
/*!40000 ALTER TABLE `club_services` DISABLE KEYS */;
INSERT INTO `club_services` (`id`, `club_id`, `name`, `price`, `unit`, `is_active`, `created_at`, `updated_at`) VALUES (1,1,'Печать А4',50.00,'лист',1,'2026-10-15 07:00:00','2026-10-15 07:00:00');
INSERT INTO `club_services` (`id`, `club_id`, `name`, `price`, `unit`, `is_active`, `created_at`, `updated_at`) VALUES (2,1,'Зарядка телефона',300.00,'час',1,'2026-10-15 07:00:00','2026-10-15 07:00:00');
INSERT INTO `club_services` (`id`, `club_id`, `name`, `price`, `unit`, `is_active`, `created_at`, `updated_at`) VALUES (3,1,'Аренда геймпада',500.00,'час',0,'2026-10-15 07:00:00','2026-10-15 07:00:00');
/*!40000 ALTER TABLE `club_services` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_subscriptions` WRITE;
/*!40000 ALTER TABLE `club_subscriptions` DISABLE KEYS */;
INSERT INTO `club_subscriptions` (`id`, `club_id`, `saas_plan_id`, `status`, `started_at`, `trial_ends_at`, `current_period_ends_at`, `canceled_at`, `provider`, `provider_reference`, `created_at`, `updated_at`) VALUES (2,1,1,'active','2026-09-01 05:00:00',NULL,'2026-11-01 05:00:00',NULL,NULL,NULL,'2026-09-01 05:00:00','2026-09-01 05:00:00');
INSERT INTO `club_subscriptions` (`id`, `club_id`, `saas_plan_id`, `status`, `started_at`, `trial_ends_at`, `current_period_ends_at`, `canceled_at`, `provider`, `provider_reference`, `created_at`, `updated_at`) VALUES (3,2,1,'expired','2026-08-01 05:00:00',NULL,'2026-10-01 05:00:00',NULL,NULL,NULL,'2026-08-01 05:00:00','2026-08-01 05:00:00');
/*!40000 ALTER TABLE `club_subscriptions` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_zone_tariff` WRITE;
/*!40000 ALTER TABLE `club_zone_tariff` DISABLE KEYS */;
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (1,1,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (1,2,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (1,4,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (2,1,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (2,2,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (2,3,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (2,6,1);
INSERT INTO `club_zone_tariff` (`club_zone_id`, `tariff_id`, `club_id`) VALUES (3,5,1);
/*!40000 ALTER TABLE `club_zone_tariff` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `club_zones` WRITE;
/*!40000 ALTER TABLE `club_zones` DISABLE KEYS */;
INSERT INTO `club_zones` (`id`, `name`, `description`, `capacity`, `price_per_hour`, `icon`, `theme`, `status`, `is_featured`, `sort_order`, `position_x`, `position_y`, `width`, `height`, `rotation`, `created_at`, `updated_at`, `club_id`) VALUES (1,'VIP-зал','Персональные игровые места',8,1000.00,'crown','green','available',1,10,20,20,390,380,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `club_zones` (`id`, `name`, `description`, `capacity`, `price_per_hour`, `icon`, `theme`, `status`, `is_featured`, `sort_order`, `position_x`, `position_y`, `width`, `height`, `rotation`, `created_at`, `updated_at`, `club_id`) VALUES (2,'Основной зал','Компьютерная зона',20,600.00,'desktop','neutral','available',0,20,425,20,330,180,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `club_zones` (`id`, `name`, `description`, `capacity`, `price_per_hour`, `icon`, `theme`, `status`, `is_featured`, `sort_order`, `position_x`, `position_y`, `width`, `height`, `rotation`, `created_at`, `updated_at`, `club_id`) VALUES (3,'Консоли','PlayStation-зона',10,NULL,'gamepad','neutral','available',0,30,770,20,300,180,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `club_zones` (`id`, `name`, `description`, `capacity`, `price_per_hour`, `icon`, `theme`, `status`, `is_featured`, `sort_order`, `position_x`, `position_y`, `width`, `height`, `rotation`, `created_at`, `updated_at`, `club_id`) VALUES (4,'Бар','Касса и напитки',NULL,NULL,'mug-hot','yellow','service',0,40,425,215,330,185,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `club_zones` (`id`, `name`, `description`, `capacity`, `price_per_hour`, `icon`, `theme`, `status`, `is_featured`, `sort_order`, `position_x`, `position_y`, `width`, `height`, `rotation`, `created_at`, `updated_at`, `club_id`) VALUES (5,'Лаунж','Ожидание и отдых',NULL,NULL,'couch','purple','available',0,50,770,215,300,185,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1);
/*!40000 ALTER TABLE `club_zones` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `clubs` WRITE;
/*!40000 ALTER TABLE `clubs` DISABLE KEYS */;
INSERT INTO `clubs` (`id`, `name`, `slug`, `settings`, `created_at`, `updated_at`, `installation_key_hash`, `installation_key_prefix`, `installation_key`, `locale`, `timezone`, `agent_exit_pin_hash`, `agent_exit_pin_changed_at`, `agent_staff_minutes`, `logo_path`, `signin_image_path`) VALUES (1,'Zafiro Arena','zafiro-arena',NULL,'2026-09-01 05:00:00','2026-09-01 05:00:00',NULL,NULL,NULL,'ru','Asia/Almaty',NULL,NULL,15,NULL,NULL);
INSERT INTO `clubs` (`id`, `name`, `slug`, `settings`, `created_at`, `updated_at`, `installation_key_hash`, `installation_key_prefix`, `installation_key`, `locale`, `timezone`, `agent_exit_pin_hash`, `agent_exit_pin_changed_at`, `agent_staff_minutes`, `logo_path`, `signin_image_path`) VALUES (2,'Zafiro Lite','zafiro-lite',NULL,'2026-08-01 05:00:00','2026-08-01 05:00:00',NULL,NULL,NULL,'ru','Asia/Almaty',NULL,NULL,15,NULL,NULL);
/*!40000 ALTER TABLE `clubs` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `computer_reservations` WRITE;
/*!40000 ALTER TABLE `computer_reservations` DISABLE KEYS */;
INSERT INTO `computer_reservations` (`id`, `club_id`, `club_computer_id`, `user_id`, `customer_name`, `customer_phone`, `note`, `starts_at`, `ends_at`, `arrived_at`, `cancelled_at`, `created_by`, `created_at`, `updated_at`) VALUES (1,1,9,10,NULL,NULL,'Придёт с другом','2026-10-15 06:30:00','2026-10-15 08:00:00',NULL,NULL,3,'2026-10-14 14:00:00','2026-10-14 14:00:00');
INSERT INTO `computer_reservations` (`id`, `club_id`, `club_computer_id`, `user_id`, `customer_name`, `customer_phone`, `note`, `starts_at`, `ends_at`, `arrived_at`, `cancelled_at`, `created_by`, `created_at`, `updated_at`) VALUES (2,1,2,NULL,'Команда Alpha','+77015550011','Турнир по CS2','2026-10-15 13:00:00','2026-10-15 16:00:00',NULL,NULL,3,'2026-10-15 04:30:00','2026-10-15 04:30:00');
INSERT INTO `computer_reservations` (`id`, `club_id`, `club_computer_id`, `user_id`, `customer_name`, `customer_phone`, `note`, `starts_at`, `ends_at`, `arrived_at`, `cancelled_at`, `created_by`, `created_at`, `updated_at`) VALUES (3,1,11,12,NULL,NULL,NULL,'2026-10-14 10:00:00','2026-10-14 11:00:00','2026-10-14 10:05:00',NULL,4,'2026-10-14 05:00:00','2026-10-14 05:00:00');
INSERT INTO `computer_reservations` (`id`, `club_id`, `club_computer_id`, `user_id`, `customer_name`, `customer_phone`, `note`, `starts_at`, `ends_at`, `arrived_at`, `cancelled_at`, `created_by`, `created_at`, `updated_at`) VALUES (4,1,12,11,NULL,NULL,NULL,'2026-10-15 11:00:00','2026-10-15 12:00:00',NULL,'2026-10-15 05:20:00',4,'2026-10-15 03:50:00','2026-10-15 03:50:00');
/*!40000 ALTER TABLE `computer_reservations` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `friendships` WRITE;
/*!40000 ALTER TABLE `friendships` DISABLE KEYS */;
/*!40000 ALTER TABLE `friendships` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `languages` WRITE;
/*!40000 ALTER TABLE `languages` DISABLE KEYS */;
INSERT INTO `languages` (`id`, `code`, `name`, `is_enabled`, `is_system`, `sort_order`, `created_at`, `updated_at`) VALUES (1,'ru','Русский',1,1,1,'2026-11-02 17:02:43','2026-11-02 17:02:43');
INSERT INTO `languages` (`id`, `code`, `name`, `is_enabled`, `is_system`, `sort_order`, `created_at`, `updated_at`) VALUES (2,'kk','Қазақша',1,1,2,'2026-11-02 17:02:43','2026-11-02 17:02:43');
INSERT INTO `languages` (`id`, `code`, `name`, `is_enabled`, `is_system`, `sort_order`, `created_at`, `updated_at`) VALUES (3,'en','English',1,1,3,'2026-11-02 17:02:43','2026-11-02 17:02:43');
/*!40000 ALTER TABLE `languages` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `member_balances` WRITE;
/*!40000 ALTER TABLE `member_balances` DISABLE KEYS */;
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (1,1,2,0.00,0.00,'2026-09-01 05:00:00','2026-09-01 05:00:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (2,1,3,0.00,0.00,'2026-09-02 06:00:00','2026-09-02 06:00:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (3,1,4,0.00,0.00,'2026-09-02 06:05:00','2026-09-02 06:05:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (4,1,5,0.00,0.00,'2026-09-02 06:10:00','2026-09-02 06:10:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (5,1,6,8500.00,250.00,'2026-09-10 13:20:00','2026-10-15 04:15:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (6,1,7,11200.00,0.00,'2026-09-12 09:00:00','2026-10-15 05:05:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (7,1,8,0.00,3000.00,'2026-09-20 15:45:00','2026-10-15 06:10:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (8,1,9,35750.50,320.00,'2026-09-21 11:30:00','2026-10-13 10:00:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (9,1,10,80.00,0.00,'2026-10-01 07:00:00','2026-10-01 07:00:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (10,1,11,0.00,0.00,'2026-10-05 14:15:00','2026-10-05 14:15:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (11,1,12,11400.00,150.00,'2026-10-08 16:40:00','2026-10-14 11:45:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (12,1,13,500.00,0.00,'2026-10-10 08:00:00','2026-10-10 08:00:00');
INSERT INTO `member_balances` (`id`, `club_id`, `user_id`, `balance`, `bonus_balance`, `created_at`, `updated_at`) VALUES (13,2,14,0.00,0.00,'2026-08-01 05:00:00','2026-08-01 05:00:00');
/*!40000 ALTER TABLE `member_balances` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `news` WRITE;
/*!40000 ALTER TABLE `news` DISABLE KEYS */;
INSERT INTO `news` (`id`, `club_id`, `title`, `body`, `image_path`, `is_published`, `created_by`, `created_at`, `updated_at`) VALUES (1,1,'Турнир по CS2 в субботу','Регистрация команд на кассе до пятницы. Призовой фонд — 100 000 ₸.',NULL,1,2,'2026-10-12 10:00:00','2026-10-12 10:00:00');
INSERT INTO `news` (`id`, `club_id`, `title`, `body`, `image_path`, `is_published`, `created_by`, `created_at`, `updated_at`) VALUES (2,1,'Новые мониторы в VIP-зале','Черновик: 240 Гц во всём VIP-зале с ноября.',NULL,0,3,'2026-10-14 07:00:00','2026-10-14 07:00:00');
INSERT INTO `news` (`id`, `club_id`, `title`, `body`, `image_path`, `is_published`, `created_by`, `created_at`, `updated_at`) VALUES (3,NULL,'Обновление Zafiro 2.4','Агент ПК 2.4.1: быстрее вход по QR и новый экран блокировки.',NULL,1,1,'2026-10-10 05:00:00','2026-10-10 05:00:00');
/*!40000 ALTER TABLE `news` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `password_recovery_codes` WRITE;
/*!40000 ALTER TABLE `password_recovery_codes` DISABLE KEYS */;
/*!40000 ALTER TABLE `password_recovery_codes` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `platform_logs` WRITE;
/*!40000 ALTER TABLE `platform_logs` DISABLE KEYS */;
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (1,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-14 05:05:00','2026-10-14 05:05:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (2,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-14 06:40:00','2026-10-14 06:40:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (3,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-14 09:12:00','2026-10-14 09:12:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (4,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-14 11:30:00','2026-10-14 11:30:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (5,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-14 14:03:00','2026-10-14 14:03:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (6,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 03:15:00','2026-10-15 03:15:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (7,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 04:00:00','2026-10-15 04:00:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (8,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 04:47:00','2026-10-15 04:47:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (9,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 05:20:00','2026-10-15 05:20:00');
INSERT INTO `platform_logs` (`id`, `user_id`, `club_id`, `event`, `description`, `subject_type`, `subject_id`, `details`, `ip_address`, `user_agent`, `created_at`, `updated_at`) VALUES (10,1,NULL,'superadmin.login','Вход суперадмина: Супер Админ',NULL,NULL,NULL,'127.0.0.1','Zafiro visual seed','2026-10-15 06:05:00','2026-10-15 06:05:00');
/*!40000 ALTER TABLE `platform_logs` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `platform_settings` WRITE;
/*!40000 ALTER TABLE `platform_settings` DISABLE KEYS */;
/*!40000 ALTER TABLE `platform_settings` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `product_categories` WRITE;
/*!40000 ALTER TABLE `product_categories` DISABLE KEYS */;
INSERT INTO `product_categories` (`id`, `name`, `created_at`, `updated_at`, `club_id`) VALUES (1,'Напитки','2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `product_categories` (`id`, `name`, `created_at`, `updated_at`, `club_id`) VALUES (2,'Снеки','2026-10-15 07:00:00','2026-10-15 07:00:00',1);
INSERT INTO `product_categories` (`id`, `name`, `created_at`, `updated_at`, `club_id`) VALUES (3,'Еда','2026-10-15 07:00:00','2026-10-15 07:00:00',1);
/*!40000 ALTER TABLE `product_categories` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `products` WRITE;
/*!40000 ALTER TABLE `products` DISABLE KEYS */;
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (1,'DR-001','Coca-Cola 0,5 л','Напитки',1,450.00,43,'2026-10-15 07:00:00','2026-10-15 04:40:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (2,'DR-002','Red Bull 0,25 л','Напитки',1,900.00,21,'2026-10-15 07:00:00','2026-10-15 05:30:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (3,'DR-003','Вода 0,5 л','Напитки',1,250.00,3,'2026-10-15 07:00:00','2026-10-15 07:00:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (4,'SN-001','Lay\'s сметана и лук','Снеки',2,600.00,27,'2026-10-15 07:00:00','2026-10-15 04:40:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (5,'SN-002','Snickers','Снеки',2,400.00,3,'2026-10-15 07:00:00','2026-10-15 06:45:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (6,'FD-001','Пицца пепперони','Еда',3,2200.00,7,'2026-10-15 07:00:00','2026-10-15 06:45:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (7,'FD-002','Хот-дог','Еда',3,900.00,10,'2026-10-15 07:00:00','2026-10-15 06:45:00',1,NULL);
INSERT INTO `products` (`id`, `sku`, `name`, `category`, `product_category_id`, `price`, `quantity`, `created_at`, `updated_at`, `club_id`, `deleted_at`) VALUES (8,'DR-004','Кофе латте','Напитки',1,750.00,0,'2026-10-15 07:00:00','2026-10-15 07:00:00',1,NULL);
/*!40000 ALTER TABLE `products` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `request_metrics` WRITE;
/*!40000 ALTER TABLE `request_metrics` DISABLE KEYS */;
/*!40000 ALTER TABLE `request_metrics` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `saas_plans` WRITE;
/*!40000 ALTER TABLE `saas_plans` DISABLE KEYS */;
INSERT INTO `saas_plans` (`id`, `slug`, `name`, `price_monthly`, `trial_days`, `features`, `is_active`, `created_at`, `updated_at`) VALUES (1,'pro','Профессиональный',25000.00,0,NULL,1,'2026-10-15 07:00:00','2026-10-15 07:00:00');
/*!40000 ALTER TABLE `saas_plans` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `server_metrics` WRITE;
/*!40000 ALTER TABLE `server_metrics` DISABLE KEYS */;
/*!40000 ALTER TABLE `server_metrics` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `session_purchases` WRITE;
/*!40000 ALTER TABLE `session_purchases` DISABLE KEYS */;
/*!40000 ALTER TABLE `session_purchases` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `shop_orders` WRITE;
/*!40000 ALTER TABLE `shop_orders` DISABLE KEYS */;
INSERT INTO `shop_orders` (`id`, `club_id`, `user_id`, `club_computer_id`, `computer_name`, `items`, `amount`, `status`, `handled_by`, `handled_at`, `created_at`, `updated_at`) VALUES (1,1,9,5,'PC-05','[{\"name\": \"Пицца пепперони\", \"price\": \"2200.00\", \"total\": \"2200.00\", \"quantity\": 1, \"product_id\": 6}]',2200.00,'done',4,'2026-10-15 05:55:00','2026-10-15 05:48:00','2026-10-15 05:55:00');
INSERT INTO `shop_orders` (`id`, `club_id`, `user_id`, `club_computer_id`, `computer_name`, `items`, `amount`, `status`, `handled_by`, `handled_at`, `created_at`, `updated_at`) VALUES (2,1,7,5,'PC-05','[{\"name\": \"Coca-Cola 0,5 л\", \"price\": \"450.00\", \"total\": \"450.00\", \"quantity\": 1, \"product_id\": 1}]',450.00,'cancelled',4,'2026-10-15 06:05:00','2026-10-15 06:02:00','2026-10-15 06:05:00');
INSERT INTO `shop_orders` (`id`, `club_id`, `user_id`, `club_computer_id`, `computer_name`, `items`, `amount`, `status`, `handled_by`, `handled_at`, `created_at`, `updated_at`) VALUES (3,1,6,1,'VIP-01','[{\"name\": \"Red Bull 0,25 л\", \"price\": \"900.00\", \"total\": \"1800.00\", \"quantity\": 2, \"product_id\": 2}, {\"name\": \"Lay\'s сметана и лук\", \"price\": \"600.00\", \"total\": \"600.00\", \"quantity\": 1, \"product_id\": 4}]',2400.00,'new',NULL,NULL,'2026-10-15 06:52:00','2026-10-15 06:52:00');
/*!40000 ALTER TABLE `shop_orders` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `stock_movements` WRITE;
/*!40000 ALTER TABLE `stock_movements` DISABLE KEYS */;
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (1,1,4,'write_off',3,45,'Продажа POS','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (2,4,4,'write_off',2,28,'Продажа POS','2026-10-13 07:20:00','2026-10-13 07:20:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (3,6,4,'write_off',2,8,'Продажа POS','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (4,2,4,'write_off',2,22,'Продажа POS','2026-10-14 08:10:00','2026-10-14 08:10:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (5,7,4,'write_off',3,12,'Продажа POS','2026-10-14 15:15:00','2026-10-14 15:15:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (6,1,4,'write_off',2,43,'Продажа POS','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (7,4,4,'write_off',1,27,'Продажа POS','2026-10-15 04:40:00','2026-10-15 04:40:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (8,2,4,'write_off',1,21,'Продажа POS','2026-10-15 05:30:00','2026-10-15 05:30:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (9,6,4,'write_off',1,7,'Продажа POS','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (10,7,4,'write_off',2,10,'Продажа POS','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
INSERT INTO `stock_movements` (`id`, `product_id`, `user_id`, `type`, `quantity`, `balance_after`, `note`, `created_at`, `updated_at`, `club_id`) VALUES (11,5,4,'write_off',1,3,'Продажа POS','2026-10-15 06:45:00','2026-10-15 06:45:00',1);
/*!40000 ALTER TABLE `stock_movements` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `tariffs` WRITE;
/*!40000 ALTER TABLE `tariffs` DISABLE KEYS */;
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (1,'1 час','Базовый тариф',60,600.00,1,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_or_bonus','KZT',0,NULL,0,NULL,0,NULL,NULL,NULL,1,NULL);
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (2,'3 часа','Выгоднее на 300 ₸',180,1500.00,1,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_or_bonus','KZT',1,NULL,0,NULL,0,NULL,NULL,NULL,1,NULL);
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (3,'Ночной пакет','Пятница и суббота, 22:00–08:00',600,2500.00,1,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_balance_only','KZT',0,NULL,0,NULL,0,NULL,NULL,'[{\"end_day\": \"sat\", \"end_time\": \"08:00\", \"start_day\": \"fri\", \"start_time\": \"22:00\"}, {\"end_day\": \"sun\", \"end_time\": \"08:00\", \"start_day\": \"sat\", \"start_time\": \"22:00\"}]',1,NULL);
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (4,'VIP 1 час',NULL,60,1000.00,1,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_balance_only','KZT',0,NULL,0,NULL,0,NULL,NULL,NULL,1,NULL);
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (5,'Консоль 2 часа',NULL,120,1400.00,1,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_or_bonus','KZT',0,NULL,0,NULL,1,2,NULL,NULL,1,NULL);
INSERT INTO `tariffs` (`id`, `name`, `description`, `duration_minutes`, `price`, `is_active`, `created_at`, `updated_at`, `payment_method`, `currency`, `discount_enabled`, `user_roles`, `usage_limit_enabled`, `usage_limit_minutes`, `quantity_limit_enabled`, `quantity_limit`, `validity_periods`, `sale_periods`, `club_id`, `deleted_at`) VALUES (6,'Летний (архив)',NULL,120,900.00,0,'2026-10-15 07:00:00','2026-10-15 07:00:00','main_or_bonus','KZT',0,NULL,0,NULL,0,NULL,NULL,NULL,1,NULL);
/*!40000 ALTER TABLE `tariffs` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `time_packages` WRITE;
/*!40000 ALTER TABLE `time_packages` DISABLE KEYS */;
/*!40000 ALTER TABLE `time_packages` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `todo_tasks` WRITE;
/*!40000 ALTER TABLE `todo_tasks` DISABLE KEYS */;
INSERT INTO `todo_tasks` (`id`, `club_id`, `title`, `due_on`, `created_by`, `done_at`, `done_by`, `created_at`, `updated_at`) VALUES (1,1,'Заказать воду у поставщика','2026-10-14',2,NULL,NULL,'2026-10-13 05:00:00','2026-10-13 05:00:00');
INSERT INTO `todo_tasks` (`id`, `club_id`, `title`, `due_on`, `created_by`, `done_at`, `done_by`, `created_at`, `updated_at`) VALUES (2,1,'Протереть клавиатуры в VIP-зале','2026-10-15',3,'2026-10-15 05:10:00',5,'2026-10-15 03:30:00','2026-10-15 05:10:00');
INSERT INTO `todo_tasks` (`id`, `club_id`, `title`, `due_on`, `created_by`, `done_at`, `done_by`, `created_at`, `updated_at`) VALUES (3,1,'Подготовить ПК к турниру','2026-10-17',2,NULL,NULL,'2026-10-15 04:00:00','2026-10-15 04:00:00');
/*!40000 ALTER TABLE `todo_tasks` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `transfer_codes` WRITE;
/*!40000 ALTER TABLE `transfer_codes` DISABLE KEYS */;
/*!40000 ALTER TABLE `transfer_codes` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `users` WRITE;
/*!40000 ALTER TABLE `users` DISABLE KEYS */;
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (1,'Супер Админ','+77010000001','root@zafiro.test',NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-01 04:00:00','2026-09-01 04:00:00',1,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (2,'Айдар Сейтказы','+77010000002','owner@zafiro.test',NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-01 05:00:00','2026-09-01 05:00:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (3,'Алия Жунусова','+77010000003',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-02 06:00:00','2026-09-02 06:00:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (4,'Ержан Касымов','+77010000004',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-02 06:05:00','2026-09-02 06:05:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (5,'Дана Ермекова','+77010000005',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-02 06:10:00','2026-09-02 06:10:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (6,'Нурлан Ахметов','+77011000006',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-10 13:20:00','2026-09-10 13:20:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (7,'Мария Иванова','+77011000007','maria@example.test',NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-12 09:00:00','2026-09-12 09:00:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (8,'Тимур Сапаров','+77011000008',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-20 15:45:00','2026-09-20 15:45:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (9,'Асель Нурланова','+77011000009',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-09-21 11:30:00','2026-09-21 11:30:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (10,'Даниил Ким','+77011000010',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-10-01 07:00:00','2026-10-01 07:00:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (11,'Жанна Омарова','+77011000011',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-10-05 14:15:00','2026-10-05 14:15:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (12,'Арман Бекетов','+77011000012',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-10-08 16:40:00','2026-10-08 16:40:00',0,NULL);
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (13,'Олжас Тулегенов','+77011000013',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-10-10 08:00:00','2026-10-10 08:00:00',0,'2026-10-14 17:00:00');
INSERT INTO `users` (`id`, `name`, `phone`, `email`, `email_verified_at`, `password`, `remember_token`, `created_at`, `updated_at`, `is_superadmin`, `blocked_at`) VALUES (14,'Марат Оспанов','+77010000014',NULL,NULL,'$2y$12$kcsi9e6mjtXK5IJ/J9FThusgS6dx185nlo1hBjj48QOnOJKDT2UUm',NULL,'2026-08-01 05:00:00','2026-08-01 05:00:00',0,NULL);
/*!40000 ALTER TABLE `users` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `webhook_deliveries` WRITE;
/*!40000 ALTER TABLE `webhook_deliveries` DISABLE KEYS */;
/*!40000 ALTER TABLE `webhook_deliveries` ENABLE KEYS */;
UNLOCK TABLES;

LOCK TABLES `webhook_endpoints` WRITE;
/*!40000 ALTER TABLE `webhook_endpoints` DISABLE KEYS */;
/*!40000 ALTER TABLE `webhook_endpoints` ENABLE KEYS */;
UNLOCK TABLES;
/*!40103 SET TIME_ZONE=@OLD_TIME_ZONE */;

/*!40101 SET SQL_MODE=@OLD_SQL_MODE */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
/*!40014 SET UNIQUE_CHECKS=@OLD_UNIQUE_CHECKS */;
/*!40111 SET SQL_NOTES=@OLD_SQL_NOTES */;

