import { CategoryScale, Chart, Legend, LinearScale, LineController, LineElement, PointElement, Tooltip, type ChartData } from 'chart.js';
import { useEffect, useMemo, useRef } from 'react';

/*
 * Графики здоровья ПК (Этап 7) — тот же Chart.js 4.5.1 из npm, цвета и шрифты осей — как у графика дашборда
 * (DashboardChart.tsx). Линии: загрузка — левая ось (%), температуры — правая (°C). Значения — только показ (не деньги).
 * Экземпляр Chart живёт, пока не меняется набор осей: новые точки (живой график — раз в 15 с) — chart.update('none')
 * без пересоздания холста.
 */

Chart.register(LineController, LineElement, PointElement, CategoryScale, LinearScale, Legend, Tooltip);

/** Серия графика: подпись (уже переведённая), значения по меткам (null — нет измерения), цвет, ось. */
export interface ChartSeries {
  readonly label: string;
  readonly data: readonly (number | null)[];
  readonly color: string;
  readonly axis: 'load' | 'temp';
}

type LineData = ChartData<'line', (number | null)[], string>;

function createChart(ctx: CanvasRenderingContext2D, showLoad: boolean, showTemp: boolean): Chart<'line', (number | null)[], string> {
  return new Chart(ctx, {
    type: 'line',
    data: { labels: [], datasets: [] },
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
          display: showLoad,
          ticks: { color: '#9ca3af', font: { size: 11 }, callback: (v) => `${String(v)} %` },
          grid: { color: 'rgba(209, 213, 219, 0.1)' },
        },
        temp: {
          position: 'right',
          display: showTemp,
          ticks: { color: '#9ca3af', font: { size: 11 }, callback: (v) => `${String(v)} °C` },
          grid: { display: false },
        },
        x: { ticks: { color: '#9ca3af', font: { size: 11 }, maxTicksLimit: 6 }, grid: { display: false } },
      },
    },
  });
}

export function HealthChart({ id, labels, series }: { readonly id: string; readonly labels: readonly string[]; readonly series: readonly ChartSeries[] }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const chart = useRef<Chart<'line', (number | null)[], string> | null>(null);
  // Серии без единого значения не показываются (и их ось тоже).
  const visible = useMemo(() => series.filter((s) => s.data.some((v) => v != null)), [series]);
  const showLoad = visible.some((s) => s.axis === 'load');
  const showTemp = visible.some((s) => s.axis === 'temp');
  const data = useMemo<LineData>(() => ({
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
  }), [labels, visible]);

  // Экземпляр — заново только при смене набора осей (их видимость — в настройках шкал).
  useEffect(() => {
    const ctx = ref.current?.getContext('2d');
    if (!ctx) return;
    const c = createChart(ctx, showLoad, showTemp);
    chart.current = c;
    return () => {
      c.destroy();
      chart.current = null;
    };
  }, [showLoad, showTemp]);

  // Данные — в существующий экземпляр, без анимации и пересоздания (эффект идёт после создания при смене осей).
  useEffect(() => {
    const c = chart.current;
    if (!c) return;
    c.data = data;
    c.update('none');
  }, [data, showLoad, showTemp]);

  return <canvas id={id} ref={ref}></canvas>;
}
