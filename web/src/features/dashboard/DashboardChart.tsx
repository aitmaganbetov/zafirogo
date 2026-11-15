import { BarController, BarElement, CategoryScale, Chart, Legend, LinearScale, Tooltip } from 'chart.js';
import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { Dashboard } from './api';

/*
 * График «Доходы и расходы за 7 дней» — Chart.js 4.5.1 (в старом — с CDN jsdelivr, здесь — из npm, Q-064),
 * настройки — дословно из @push('scripts') dashboard.blade.php. В график идут целые тиыны; в тенге они
 * переводятся только при выводе (подписи оси и подсказка) — вид тот же, что у старого графика в тенге
 * (шаг оси в тиынах = шаг в тенге × 100).
 */

Chart.register(BarController, BarElement, CategoryScale, LinearScale, Legend, Tooltip);

/** Тиыны → тенге только для подписи (число делений оси может быть дробным в тиынах — это не сумма). */
const tengeLabel = (tiyn: number, f: Intl.NumberFormat) => f.format(tiyn / 100);

export function DashboardChart({ days }: { days: Dashboard['days'] }) {
  const { t } = useTranslation();
  const ref = useRef<HTMLCanvasElement>(null);
  const income = t('Доход');
  const expense = t('Расход');
  useEffect(() => {
    const el = ref.current;
    const ctx = el?.getContext('2d');
    if (!el || !ctx) return;
    const chart = new Chart(ctx, {
      type: 'bar',
      data: {
        labels: days.map((d) => d.label),
        datasets: [
          {
            label: income,
            data: days.map((d) => d.income_tiyn),
            backgroundColor: 'rgba(34, 197, 94, 0.75)',
            borderColor: 'rgba(22, 163, 74, 1)',
            borderWidth: 1,
            borderRadius: 8,
          },
          {
            label: expense,
            data: days.map((d) => d.expenses_tiyn),
            backgroundColor: 'rgba(244, 63, 94, 0.7)',
            borderColor: 'rgba(225, 29, 72, 1)',
            borderWidth: 1,
            borderRadius: 8,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        plugins: {
          legend: {
            position: 'bottom',
            labels: { padding: 20, usePointStyle: true, color: '#6b7280', font: { size: 12, weight: 'bold' } },
          },
          // Как подсказка Chart.js по умолчанию («Доход: 12 345,5»), но из тиынов.
          tooltip: { callbacks: { label: (c) => `${c.dataset.label ?? ''}: ${tengeLabel(Number(c.parsed.y), new Intl.NumberFormat(c.chart.options.locale))}` } },
        },
        scales: {
          y: {
            beginAtZero: true,
            ticks: {
              color: '#9ca3af',
              callback: (value) => tengeLabel(Number(value), new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 0 })),
              font: { size: 11 },
            },
            grid: { color: 'rgba(209, 213, 219, 0.1)' },
          },
          x: { ticks: { color: '#9ca3af', font: { size: 11 } }, grid: { display: false } },
        },
      },
    });
    return () => chart.destroy();
  }, [days, income, expense]);
  return <canvas id="chart" ref={ref} className="dashboard-chart"></canvas>;
}
