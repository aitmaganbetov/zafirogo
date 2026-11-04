import { CategoryScale, Chart, Legend, LinearScale, LineController, LineElement, PointElement, Tooltip } from 'chart.js';
import { useEffect, useRef } from 'react';

/*
 * Графики здоровья ПК (Этап 7) — тот же Chart.js 4.5.1 из npm и тот же подход, что у графика дашборда
 * (DashboardChart.tsx: экземпляр в useEffect, destroy при смене данных, цвета и шрифты осей оттуда же). Линии: загрузка —
 * левая ось (%), температуры — правая (°C). Значения — только показ (не деньги).
 */

Chart.register(LineController, LineElement, PointElement, CategoryScale, LinearScale, Legend, Tooltip);

/** Серия графика: подпись (уже переведённая), значения по меткам (null — нет измерения), цвет, ось. */
export interface ChartSeries {
  readonly label: string;
  readonly data: readonly (number | null)[];
  readonly color: string;
  readonly axis: 'load' | 'temp';
}

export function HealthChart({ id, labels, series }: { readonly id: string; readonly labels: readonly string[]; readonly series: readonly ChartSeries[] }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const el = ref.current;
    const ctx = el?.getContext('2d');
    if (!el || !ctx) return;
    const visible = series.filter((s) => s.data.some((v) => v != null));
    const chart = new Chart(ctx, {
      type: 'line',
      data: {
        labels: [...labels],
        datasets: visible.map((s) => ({
          label: s.label,
          data: [...s.data],
          borderColor: s.color,
          backgroundColor: s.color,
          borderWidth: 2,
          pointRadius: 0,
          spanGaps: false,
          yAxisID: s.axis,
        })),
      },
      options: {
        responsive: true,
        maintainAspectRatio: true,
        animation: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: {
            position: 'bottom',
            labels: { padding: 12, usePointStyle: true, color: '#6b7280', font: { size: 11, weight: 'bold' } },
          },
        },
        scales: {
          load: {
            position: 'left',
            min: 0,
            max: 100,
            display: visible.some((s) => s.axis === 'load'),
            ticks: { color: '#9ca3af', font: { size: 11 }, callback: (v) => `${String(v)} %` },
            grid: { color: 'rgba(209, 213, 219, 0.1)' },
          },
          temp: {
            position: 'right',
            display: visible.some((s) => s.axis === 'temp'),
            ticks: { color: '#9ca3af', font: { size: 11 }, callback: (v) => `${String(v)} °C` },
            grid: { display: false },
          },
          x: { ticks: { color: '#9ca3af', font: { size: 11 }, maxTicksLimit: 6 }, grid: { display: false } },
        },
      },
    });
    return () => chart.destroy();
  }, [labels, series]);
  return <canvas id={id} ref={ref}></canvas>;
}
