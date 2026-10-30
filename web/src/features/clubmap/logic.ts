/*
 * Чистая логика карты клуба — перенос inline-скриптов club-map/index.blade.php: сетки и пересечения карточек,
 * классы ПК, предпросмотр суммы гостевой сессии (в тиынах, решение Q-019), длительности брони.
 */

/** Сетка зон (drag/resize) и минимальные размеры (UpdateClubZoneLayoutRequest; диван меньше). */
export const ZONE_GRID = 20;
export const ZONE_GAP = 20;
/** Сетка ПК в зоне: шаг 47×42, отступ 5 (ClubZoneComputerService). */
export const PC_GRID_X = 47;
export const PC_GRID_Y = 42;
export const PC_OFFSET = 5;
export const MIN_CANVAS = 430;

export interface Rect {
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
}

/** Высота плана: max(430, низ самой нижней зоны + 30). */
export function canvasHeight(rects: readonly Rect[]): number {
  return rects.reduce((h, r) => Math.max(h, r.y + r.height + 30), MIN_CANVAS);
}

/** Пересечение карточки с другими (зазор 20) — collides() шаблона. */
export function collides(others: readonly Rect[], x: number, y: number, width: number, height: number): boolean {
  return others.some((o) => x < o.x + o.width + ZONE_GAP && x + width + ZONE_GAP > o.x && y < o.y + o.height + ZONE_GAP && y + height + ZONE_GAP > o.y);
}

/** Позиция зоны при перетаскивании: шаг 20, не меньше 0. */
export function snapZone(start: number, delta: number): number {
  return Math.max(0, Math.round((start + delta) / ZONE_GRID) * ZONE_GRID);
}

/** Размер зоны при изменении: шаг 20, минимум (диван 80×60, иначе 180×140 — как JS шаблона). */
export function snapSize(start: number, delta: number, min: number): number {
  return Math.max(min, Math.round((start + delta) / ZONE_GRID) * ZONE_GRID);
}

export function zoneMinSize(sofa: boolean): { width: number; height: number } {
  return sofa ? { width: 80, height: 60 } : { width: 180, height: 140 };
}

/** Ячейка ПК при перетаскивании внутри зоны (сетка 47×42 от отступа 5, в пределах области). */
export function snapComputer(start: number, delta: number, grid: number, limit: number): number {
  const max = limit >= PC_OFFSET ? PC_OFFSET + Math.floor((limit - PC_OFFSET) / grid) * grid : 0;
  const snap = PC_OFFSET + Math.round((start + delta - PC_OFFSET) / grid) * grid;
  return Math.min(max, Math.max(PC_OFFSET, snap));
}

/** Классы ПК на карте: статус и подсветка (computerStates: trim($status.' '.$light)) + выбор. */
export function computerClasses(status: string, light: string, selected: boolean): string {
  return ['cm-computer', status, light, selected ? 'selected' : ''].filter(Boolean).join(' ');
}

/**
 * Предпросмотр «К оплате» гостевой сессии в тиынах (Q-019; итог считает сервер). Поминутно — за каждый ПК
 * intdiv(цена часа · минуты + 30, 60) (как pricing.MinutesPrice и JS шаблона ~стр. 138); у ПК без цены часа —
 * missing. Тариф — цена × число ПК.
 */
export function guestPreview(
  choice: { kind: 'minutes'; minutes: number } | { kind: 'tariff'; priceTiyn: number },
  hourPrices: readonly (number | null | undefined)[],
): { missing: boolean; sumTiyn: number } {
  if (choice.kind === 'tariff') return { missing: false, sumTiyn: choice.priceTiyn * hourPrices.length };
  const minutes = Number.isSafeInteger(choice.minutes) && choice.minutes > 0 ? choice.minutes : 0;
  let sum = 0;
  let missing = false;
  for (const hour of hourPrices) {
    if (hour == null) {
      missing = true;
      continue;
    }
    sum += Math.floor((hour * minutes + 30) / 60);
  }
  return { missing, sumTiyn: sum };
}

/**
 * Сумма предпросмотра как Intl.NumberFormat(locale, {maximumFractionDigits: 2}) шаблона, но из целых тиынов:
 * дробная часть без лишних нулей.
 */
export function formatPreviewSum(tiyn: number, locale: string): string {
  const neg = tiyn < 0;
  const abs = Math.abs(tiyn);
  const nf = new Intl.NumberFormat(locale, { maximumFractionDigits: 0 });
  const whole = nf.format(Math.floor(abs / 100));
  const frac = abs % 100;
  let out = whole;
  if (frac !== 0) {
    const dec = new Intl.NumberFormat(locale, { minimumFractionDigits: 1 }).formatToParts(1.5).find((p) => p.type === 'decimal')?.value ?? ',';
    out += dec + (frac % 10 === 0 ? String(frac / 10) : String(frac).padStart(2, '0'));
  }
  return (neg ? '-' : '') + out;
}

/** Длительности брони с карты (минуты) и выбранная по умолчанию. */
export const BOOKING_DURATIONS = [30, 60, 90, 120, 180, 240, 360, 480, 720] as const;
export const DEFAULT_BOOKING_DURATION = 60;
