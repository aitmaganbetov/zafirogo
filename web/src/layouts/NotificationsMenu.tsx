import { useCallback, useEffect, useRef, useState } from 'react';
import type { MouseEvent as ReactMouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { STORAGE_KEYS, readStorage, writeStorage } from './storage';
import type { NotificationItem, NotificationsPayload, NotificationsSource } from './types';

/*
 * Колокольчик уведомлений — перенос .admin-notifications и его inline-скрипта из
 * layouts/admin.blade.php: список, точка непрочитанного, отметка «просмотрено»
 * (POST /notifications/seen), звуковой сигнал Web Audio без файла, память уже прозвучавших
 * уведомлений в браузере (zafiro.notificationsHeard, последние 200), выключатель звука
 * (zafiro.notificationSound).
 *
 * Обновление: в старом проекте — опрос GET /notifications раз в 60 с при открытой вкладке и сразу
 * при возврате на неё. Здесь — подписка source.subscribe (WebSocket); если подписки нет, опрос
 * оставлен как в старом проекте (временный запасной вариант).
 */

const POLL_MS = 60_000;
const HEARD_LIMIT = 200;

type AudioContextConstructor = typeof AudioContext;

function audioContextClass(): AudioContextConstructor | undefined {
  const w = window as Window & { webkitAudioContext?: AudioContextConstructor };
  return 'AudioContext' in window ? window.AudioContext : w.webkitAudioContext;
}

function readHeard(): Set<string> {
  try {
    const parsed: unknown = JSON.parse(readStorage(STORAGE_KEYS.notificationsHeard) ?? '[]');
    return new Set(Array.isArray(parsed) ? parsed.map((value) => String(value)) : []);
  } catch {
    return new Set();
  }
}

export function NotificationsMenu({ source }: { readonly source: NotificationsSource | null }) {
  const { t } = useTranslation();
  const rootRef = useRef<HTMLDivElement>(null);
  const [items, setItems] = useState<readonly NotificationItem[]>([]);
  const [unread, setUnread] = useState(0);
  // «Уведомлений нет» — только после первой загрузки (в старом layout элемент скрыт до неё).
  const [loaded, setLoaded] = useState(false);
  const [open, setOpen] = useState(false);
  const [soundOn, setSoundOn] = useState(() => readStorage(STORAGE_KEYS.notificationSound) !== '0');

  const soundOnRef = useRef(soundOn);
  const audioRef = useRef<AudioContext | null>(null);
  const pendingRef = useRef(false);
  const heardRef = useRef<Set<string>>(readHeard());

  useEffect(() => {
    soundOnRef.current = soundOn;
  }, [soundOn]);

  // Короткий двухтоновый сигнал (880 Гц и 1318,5 Гц), как в старом layout.
  const chime = useCallback(() => {
    if (!soundOnRef.current) return;
    const context = audioRef.current;
    if (context === null || context.state !== 'running') {
      pendingRef.current = true;
      return;
    }
    const start = context.currentTime;
    for (const [frequency, delay] of [[880, 0], [1318.5, 0.15]] as const) {
      const tone = context.createOscillator();
      const gain = context.createGain();
      tone.type = 'sine';
      tone.frequency.value = frequency;
      gain.gain.setValueAtTime(0.0001, start + delay);
      gain.gain.exponentialRampToValueAtTime(0.2, start + delay + 0.02);
      gain.gain.exponentialRampToValueAtTime(0.0001, start + delay + 0.4);
      tone.connect(gain).connect(context.destination);
      tone.start(start + delay);
      tone.stop(start + delay + 0.45);
    }
  }, []);

  // Браузер разрешает звук только после действия пользователя: сигнал, пришедший раньше,
  // звучит при первом нажатии.
  const unlock = useCallback(() => {
    const Context = audioContextClass();
    if (Context === undefined) return;
    audioRef.current ??= new Context();
    const context = audioRef.current;
    (context.state === 'suspended' ? context.resume() : Promise.resolve())
      .then(() => {
        if (pendingRef.current) {
          pendingRef.current = false;
          chime();
        }
      })
      .catch(() => undefined);
  }, [chime]);

  useEffect(() => {
    const options = { once: true, capture: true } as const;
    document.addEventListener('pointerdown', unlock, options);
    document.addEventListener('keydown', unlock, options);
    return () => {
      document.removeEventListener('pointerdown', unlock, options);
      document.removeEventListener('keydown', unlock, options);
    };
  }, [unlock]);

  const apply = useCallback(
    (payload: NotificationsPayload) => {
      setUnread(payload.unread_count);
      setItems(payload.items);
      setLoaded(true);
      const heard = heardRef.current;
      const fresh = payload.items.filter((item) => item.unread && !heard.has(item.id));
      if (fresh.length === 0) return;
      fresh.forEach((item) => heard.add(item.id));
      writeStorage(STORAGE_KEYS.notificationsHeard, JSON.stringify([...heard].slice(-HEARD_LIMIT)));
      chime();
    },
    [chime],
  );

  useEffect(() => {
    if (source === null) return;
    let alive = true;
    const load = () => {
      source
        .load()
        .then((payload) => {
          if (alive) apply(payload);
        })
        .catch(() => undefined);
    };
    load();
    if (source.subscribe !== undefined) {
      const unsubscribe = source.subscribe((payload) => {
        if (alive) apply(payload);
      });
      return () => {
        alive = false;
        unsubscribe();
      };
    }
    // Запасной вариант без WebSocket: как в старом layout — раз в минуту и при возврате на вкладку.
    const timer = window.setInterval(() => {
      if (!document.hidden) load();
    }, POLL_MS);
    const onVisible = () => {
      if (!document.hidden) load();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      alive = false;
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [source, apply]);

  useEffect(() => {
    if (!open) return;
    const onClick = (event: MouseEvent) => {
      if (event.target instanceof Node && !rootRef.current?.contains(event.target)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('click', onClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('click', onClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const togglePanel = (event: ReactMouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    const opening = !open;
    setOpen(opening);
    if (opening && unread > 0) {
      setUnread(0);
      source?.markSeen().catch(() => undefined);
    }
  };

  const toggleSound = (event: ReactMouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    const next = !soundOn;
    soundOnRef.current = next;
    setSoundOn(next);
    writeStorage(STORAGE_KEYS.notificationSound, next ? '1' : '0');
    if (next) {
      unlock();
      window.setTimeout(chime, 50);
    }
  };

  const soundLabel = soundOn ? t('Звук уведомлений включён') : t('Звук уведомлений выключен');

  return (
    <div className="admin-notifications" data-notifications ref={rootRef}>
      <button
        type="button"
        id="notifications-button"
        className="admin-icon-button"
        aria-label={t('Уведомления')}
        aria-haspopup="true"
        aria-expanded={open}
        aria-controls="notifications-panel"
        onClick={togglePanel}
      >
        <i className="far fa-bell"></i>
        <span className="dot" data-notifications-dot hidden={unread === 0}></span>
      </button>
      <div id="notifications-panel" className="admin-notifications-panel" role="region" aria-label={t('Уведомления')} hidden={!open}>
        <div className="admin-notifications-head">
          <strong>{t('Уведомления')}</strong>
          <button type="button" className="admin-notifications-sound" data-notifications-sound aria-pressed={soundOn} aria-label={soundLabel} title={soundLabel} onClick={toggleSound}>
            <i className={soundOn ? 'fas fa-volume-high' : 'fas fa-volume-xmark'} aria-hidden="true"></i>
          </button>
        </div>
        <ul className="admin-notifications-list" data-notifications-list>
          {items.map((item) => (
            <li key={item.id}>
              <a href={item.url} className={item.unread ? 'admin-notification unread' : 'admin-notification'}>
                <i className={`fas ${item.icon}`} aria-hidden="true"></i>
                <span>
                  <strong>{item.title}</strong>
                  <span>{item.body}</span>
                  <small>{item.at_human}</small>
                </span>
              </a>
            </li>
          ))}
        </ul>
        <p className="admin-notifications-empty" data-notifications-empty hidden={!loaded || items.length > 0}>
          {t('Уведомлений нет')}
        </p>
      </div>
    </div>
  );
}
