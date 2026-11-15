import type { components } from '../../api/gen/superadmin';

/** Ответ GET /api/superadmin/dashboard стенда визуального сравнения (демо-данные tools/visual), укороченный: тесты экрана «Сервер». */
export const dashboardFixture: components['schemas']['Dashboard'] = {
  "agent_download_url": null,
  "agent_versions": [
    {
      "count": 9,
      "outdated": false,
      "version": "2.4.1"
    },
    {
      "count": 1,
      "outdated": false,
      "version": "2.3.0"
    },
    {
      "count": 1,
      "outdated": false,
      "version": "2.1.7"
    }
  ],
  "backup": {
    "at_human": null,
    "configured": false,
    "stale": true
  },
  "certificate_hosts": [],
  "channels": [
    {
      "channel": "panel",
      "errors_percent": null,
      "has_errors": false,
      "label": "Панель",
      "requests_per_minute": "0",
      "response_ms": null
    },
    {
      "channel": "agent",
      "errors_percent": null,
      "has_errors": false,
      "label": "Агенты ПК",
      "requests_per_minute": "0",
      "response_ms": null
    },
    {
      "channel": "api",
      "errors_percent": null,
      "has_errors": false,
      "label": "API v1",
      "requests_per_minute": "0",
      "response_ms": null
    }
  ],
  "charts": {
    "clubs": {
      "from_label": "14.10 07:00",
      "lines": [],
      "max_label": "1",
      "to_label": "15.10 07:00"
    },
    "duration": {
      "from_label": "14.10 07:00",
      "lines": [
        {
          "color": "#7c3aed",
          "label": "Панель",
          "segments": [],
          "translate": true
        },
        {
          "color": "#0ea5e9",
          "label": "Агенты ПК",
          "segments": [],
          "translate": true
        },
        {
          "color": "#f59e0b",
          "label": "API v1",
          "segments": [],
          "translate": true
        }
      ],
      "max_label": "1",
      "to_label": "15.10 07:00"
    },
    "errors": {
      "from_label": "14.10 07:00",
      "lines": [
        {
          "color": "#7c3aed",
          "label": "Ошибки 5xx, %",
          "segments": [],
          "translate": true
        }
      ],
      "max_label": "1",
      "to_label": "15.10 07:00"
    },
    "load": {
      "from_label": "14.10 07:00",
      "lines": [
        {
          "color": "#7c3aed",
          "label": "Нагрузка CPU",
          "segments": [],
          "translate": true
        }
      ],
      "max_label": "4.4",
      "to_label": "15.10 07:00"
    },
    "memory": {
      "from_label": "14.10 07:00",
      "lines": [
        {
          "color": "#7c3aed",
          "label": "Память, %",
          "segments": [],
          "translate": true
        }
      ],
      "max_label": "100",
      "to_label": "15.10 07:00"
    },
    "requests": {
      "from_label": "14.10 07:00",
      "lines": [
        {
          "color": "#7c3aed",
          "label": "Панель",
          "segments": [],
          "translate": true
        },
        {
          "color": "#0ea5e9",
          "label": "Агенты ПК",
          "segments": [],
          "translate": true
        },
        {
          "color": "#f59e0b",
          "label": "API v1",
          "segments": [],
          "translate": true
        }
      ],
      "max_label": "1",
      "to_label": "15.10 07:00"
    }
  },
  "club_load": [],
  "computers": {
    "online": 8,
    "total": 12
  },
  "latest_agent": null,
  "ledger_reconcile": {
    "at_human": "1 минуту назад",
    "open_findings": 0,
    "stale": false
  },
  "live": {
    "cpu_count": 4,
    "database_mb": 20,
    "disk_alert_percent": 85,
    "disk_percent": 97,
    "disk_total_gb": "252",
    "disk_used_gb": "245",
    "load_1": "3.16",
    "load_15": "2.77",
    "load_5": "4.19",
    "load_alert": true,
    "memory_percent": 14,
    "memory_total_gb": "15.7",
    "memory_used_gb": "2.2",
    "queue_failed": 0,
    "queue_oldest_minutes": null,
    "queue_pending": 0,
    "queue_pending_alert": 100,
    "uptime_days": 0
  },
  "log_errors": [],
  "period": "24h",
  "period_minutes": 1440,
  "recent_logs": [
    {
      "at_utc": "15.10.2026 07:00",
      "club_id": null,
      "club_name": "",
      "description": "У пользователя Айдар Сейтказы сняты права суперадмина",
      "event": "superadmin.revoked",
      "event_label": "Снятие прав суперадмина",
      "id": 26,
      "ip_address": "127.0.0.1",
      "system": false,
      "user_name": "Супер Админ"
    },
    {
      "at_utc": "15.10.2026 07:00",
      "club_id": null,
      "club_name": "",
      "description": "Пользователю Айдар Сейтказы выданы права суперадмина",
      "event": "superadmin.granted",
      "event_label": "Выдача прав суперадмина",
      "id": 25,
      "ip_address": "127.0.0.1",
      "system": false,
      "user_name": "Супер Админ"
    },
    {
      "at_utc": "15.11.2026 16:23",
      "club_id": null,
      "club_name": "",
      "description": "Неверный код двухфакторной аутентификации: Супер Админ",
      "event": "superadmin.totp_failed",
      "event_label": "Неверный код двухфакторной аутентификации",
      "id": 24,
      "ip_address": "127.0.0.1",
      "system": false,
      "user_name": "Супер Админ"
    }
  ],
  "scheduler_minutes_ago": null,
  "scheduler_silent_minutes": 5,
  "selected_clubs": [],
  "tables": [
    {
      "megabytes": "2.5",
      "name": "translations",
      "rows": 7428
    },
    {
      "megabytes": "0.2",
      "name": "club_computers",
      "rows": 0
    },
    {
      "megabytes": "0.1",
      "name": "cash_transactions",
      "rows": 0
    }
  ],
  "today": "2026-10-15"
};
