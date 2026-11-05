import type { TFunction } from 'i18next';
import type { ComputerState, HealthProblem, HealthVerdictReason, HealthWeek } from './api';

/* Тексты состояния ПК (Этап 7): проценты, подписи проблем и периферии, длительности. Ключи — строковые литералы (проверка переводов). */

/** Сотые доли процента → целые проценты (0..100). */
export const percent = (bp: number) => Math.min(100, Math.max(0, Math.round(bp / 100)));

/** То же для необязательного значения: нет значения — null (точка графика без измерения). */
export const pctOrNull = (bp?: number | null): number | null => (bp == null ? null : percent(bp));

/**
 * Десятые доли °C → целые °C; нет значения — null. zeroIsMissing — 0 тоже «нет данных» (живые температуры: агент шлёт 0,
 * если датчика нет); в истории 0 — значение.
 */
export const degOrNull = (deci?: number | null, zeroIsMissing = false): number | null =>
  deci == null || (zeroIsMissing && deci === 0) ? null : Math.round(deci / 10);

/** SMART тома: «SMART: отказ» / «SMART: предупреждение»; исправен или не измерен — пусто. */
export function smartLabel(t: TFunction, smart: string | null | undefined): string {
  switch (smart) {
    case 'failing':
      return t('SMART: отказ');
    case 'warning':
      return t('SMART: предупреждение');
    default:
      return '';
  }
}

/**
 * Подпись проблемы здоровья (ключи — литералы для проверки переводов). «Медленная сеть» — одна подпись для всех ПК
 * (у бездискового порог строже, S7-H-18, но проблема та же).
 */
export function problemLabel(t: TFunction, kind: HealthProblem | string): string {
  switch (kind) {
    case 'cpu_hot':
      return t('Перегрев CPU');
    case 'gpu_hot':
      return t('Перегрев GPU');
    case 'disk_low':
      return t('Мало места на диске');
    case 'disk_smart':
      return t('Диск неисправен (SMART)');
    case 'mouse':
      return t('Мышь не подключена');
    case 'keyboard':
      return t('Клавиатура не подключена');
    case 'monitor':
      return t('Монитор не подключён');
    case 'network_slow':
      return t('Медленная сеть');
    case 'unstable':
      return t('Нестабильная работа: сбои за 7 суток');
    default:
      return kind;
  }
}

export function peripheralLabel(t: TFunction, kind: string): string {
  switch (kind) {
    case 'mouse':
      return t('Мышь');
    case 'keyboard':
      return t('Клавиатура');
    case 'headset':
      return t('Гарнитура');
    case 'monitor':
      return t('Монитор');
    default:
      return kind;
  }
}

/** Подсказка карточки ПК: «CPU 42 % · ОЗУ 61 %» (нет данных — пусто). */
export function loadTitle(t: TFunction, state: ComputerState | undefined): string {
  if (!state?.load || state.connection !== 'online') return '';
  return [t('CPU {{value}} %', { value: percent(state.load.cpu_bp) }), t('ОЗУ {{value}} %', { value: percent(state.load.ram_bp) })].join(' · ');
}

/** Время работы: «2 д 3 ч», «5 ч 12 мин», «7 мин». */
export function uptime(t: TFunction, seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return t('{{days}} д {{hours}} ч', { days: d, hours: h });
  if (h > 0) return t('{{hours}} ч {{minutes}} мин', { hours: h, minutes: m });
  return t('{{minutes}} мин', { minutes: m });
}

/** Вердикт исправности за неделю (S7-H-15). */
export function verdictLabel(t: TFunction, level: HealthWeek['verdict']['level']): string {
  switch (level) {
    case 'repair':
      return t('Требует ремонта');
    case 'watch':
      return t('Наблюдать');
    default:
      return t('Исправен');
  }
}

/** Причина вердикта: ключ перевода с подстановками (value/threshold — числа сервера). */
export function reasonLabel(t: TFunction, r: HealthVerdictReason): string {
  switch (r.code) {
    case 'smart_failing':
      return smartLabel(t, 'failing');
    case 'smart_warning':
      return smartLabel(t, 'warning');
    case 'bugchecks':
      return t('«Синих экранов» за неделю: {{count}}', { count: r.value });
    case 'shutdowns':
      return t('Внезапных выключений за неделю: {{count}}', { count: r.value });
    case 'cpu_hot':
      return t('Перегрев CPU (от {{threshold}} °C): {{hours}} ч', { threshold: r.threshold, hours: r.value });
    case 'gpu_hot':
      return t('Перегрев GPU (от {{threshold}} °C): {{hours}} ч', { threshold: r.threshold, hours: r.value });
    case 'disk_low':
      return t('Системный диск: свободно {{value}} % (меньше {{threshold}} %)', { value: r.value, threshold: r.threshold });
    case 'cpu_temp_rise':
      return t('Температура CPU выросла за неделю на {{value}} °C', { value: r.value });
    case 'gpu_temp_rise':
      return t('Температура GPU выросла за неделю на {{value}} °C', { value: r.value });
    case 'network_slow':
      return t('Медленная сеть (меньше {{threshold}} Мбит/с): {{hours}} ч', { threshold: r.threshold, hours: r.value });
    default:
      return r.code;
  }
}
