import { useTranslation } from 'react-i18next';
import type { ComputerState } from './api';
import { percent, problemLabel } from './healthText';

/*
 * Отдельно от ComputerHealth.tsx: плитке ПК на карте не нужен Chart.js — раздел «Состояние» с графиком
 * подгружается только при открытии панели ПК (FE-08).
 */

/** Загрузка и проблемы на карточке ПК: две полоски (CPU, ОЗУ) и значок с подсказкой. Нет данных — ничего. */
export function PcLoad({ state }: { readonly state: ComputerState | undefined }) {
  const { t } = useTranslation();
  if (!state || state.connection !== 'online') return null;
  const problems = state.problems ?? [];
  return (
    <>
      {state.load && (
        <span className="cm-pc-load" aria-hidden="true">
          <i style={{ width: `${percent(state.load.cpu_bp)}%` }}></i>
          <i style={{ width: `${percent(state.load.ram_bp)}%` }}></i>
        </span>
      )}
      {problems.length > 0 && (
        <span className="cm-pc-alert" title={problems.map((p) => problemLabel(t, p)).join(', ')} aria-label={problems.map((p) => problemLabel(t, p)).join(', ')}>
          <i className="fas fa-triangle-exclamation"></i>
        </span>
      )}
    </>
  );
}
