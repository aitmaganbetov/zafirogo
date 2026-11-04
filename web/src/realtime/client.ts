import { unwrap } from '../api/http';
import { authApi } from '../auth/session';

/*
 * WebSocket админки (docs/realtime.md): GET /ws?ticket=… — билет из POST /api/ws-ticket (одноразовый, 30 с;
 * решение EV-3). Сообщения: {id, type, at, data}; первое — hello. Клиент ничего не отправляет.
 * Переподключение с экспоненциальной задержкой (1 → 30 с, со случайным разбросом); код 4001 — сразу с новым
 * билетом (права изменились). После переподключения подписчики перечитывают данные по REST (onReconnect):
 * пропущенные события не досылаются.
 */

export interface RealtimeMessage<D = unknown> {
  readonly id?: string;
  readonly type: string;
  readonly at: string;
  readonly data: D;
}

export type RealtimeHandler = (msg: RealtimeMessage) => void;
export type ConnectionState = 'idle' | 'connecting' | 'open' | 'closed';

const CLOSE_REAUTH = 4001;
const MIN_DELAY = 1000;
const MAX_DELAY = 30_000;
const SEEN_LIMIT = 500;

/** Совпадает ли тип с шаблоном: точный ('chat.unread'), раздел ('chat.*') или все ('*'). */
export function matchType(pattern: string, type: string): boolean {
  if (pattern === '*' || pattern === type) return true;
  return pattern.endsWith('.*') && type.startsWith(pattern.slice(0, -1));
}

export interface RealtimeOptions {
  /** Билет для подключения (по умолчанию POST /api/ws-ticket). */
  readonly ticket?: () => Promise<string>;
  /** Конструктор сокета (тесты). */
  readonly createSocket?: (url: string) => WebSocket;
  readonly url?: (ticket: string) => string;
  readonly random?: () => number;
}

export class RealtimeClient {
  private socket: WebSocket | null = null;
  private readonly handlers = new Set<{ pattern: string; fn: RealtimeHandler }>();
  private readonly reconnectHandlers = new Set<() => void>();
  private readonly stateHandlers = new Set<(s: ConnectionState) => void>();
  private readonly seen: string[] = [];
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private running = false;
  private connectedOnce = false;
  private state: ConnectionState = 'idle';
  private readonly o: Required<RealtimeOptions>;

  constructor(o: RealtimeOptions = {}) {
    this.o = {
      ticket: o.ticket ?? (async () => (await unwrap(authApi.POST('/api/ws-ticket'))).ticket),
      createSocket: o.createSocket ?? ((url) => new WebSocket(url)),
      url:
        o.url ??
        ((ticket) => `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/ws?ticket=${encodeURIComponent(ticket)}`),
      random: o.random ?? Math.random,
    };
  }

  /** Подписка на события по шаблону типа; возвращает отписку. */
  on(pattern: string, fn: RealtimeHandler): () => void {
    const h = { pattern, fn };
    this.handlers.add(h);
    return () => this.handlers.delete(h);
  }

  /** После переподключения (не первого подключения): перечитать данные экрана. */
  onReconnect(fn: () => void): () => void {
    this.reconnectHandlers.add(fn);
    return () => this.reconnectHandlers.delete(fn);
  }

  onState(fn: (s: ConnectionState) => void): () => void {
    this.stateHandlers.add(fn);
    return () => this.stateHandlers.delete(fn);
  }

  getState(): ConnectionState {
    return this.state;
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    void this.connect();
  }

  stop(): void {
    this.running = false;
    this.connectedOnce = false;
    this.attempt = 0;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    const s = this.socket;
    this.socket = null;
    s?.close(1000);
    this.setState('idle');
  }

  /** Разослать сообщение подписчикам (с дедупликацией по id). */
  dispatch(msg: RealtimeMessage): void {
    if (msg.id) {
      if (this.seen.includes(msg.id)) return;
      this.seen.push(msg.id);
      if (this.seen.length > SEEN_LIMIT) this.seen.shift();
    }
    for (const h of this.handlers) {
      if (matchType(h.pattern, msg.type)) {
        try {
          h.fn(msg);
        } catch (e) {
          console.error('realtime handler', msg.type, e);
        }
      }
    }
  }

  private setState(s: ConnectionState): void {
    this.state = s;
    for (const fn of this.stateHandlers) fn(s);
  }

  /** Задержка переподключения: 1, 2, 4 … 30 с ± 30 %. */
  delay(attempt: number): number {
    const base = Math.min(MAX_DELAY, MIN_DELAY * 2 ** attempt);
    return Math.round(base * (0.7 + 0.6 * this.o.random()));
  }

  private schedule(immediate = false): void {
    if (!this.running) return;
    const wait = immediate ? 0 : this.delay(this.attempt++);
    this.timer = setTimeout(() => void this.connect(), wait);
  }

  private async connect(): Promise<void> {
    if (!this.running) return;
    this.setState('connecting');
    let ticket: string;
    try {
      ticket = await this.o.ticket();
    } catch {
      this.setState('closed');
      this.schedule();
      return;
    }
    if (!this.running) return;
    const ws = this.o.createSocket(this.o.url(ticket));
    this.socket = ws;
    ws.onmessage = (ev: MessageEvent) => {
      let msg: RealtimeMessage;
      try {
        msg = JSON.parse(String(ev.data)) as RealtimeMessage;
      } catch {
        return;
      }
      if (msg.type === 'hello') {
        this.attempt = 0;
        this.setState('open');
        if (this.connectedOnce) for (const fn of this.reconnectHandlers) fn();
        this.connectedOnce = true;
        return;
      }
      this.dispatch(msg);
    };
    ws.onclose = (ev: CloseEvent) => {
      if (this.socket !== ws) return;
      this.socket = null;
      this.setState('closed');
      this.schedule(ev.code === CLOSE_REAUTH);
    };
  }
}
