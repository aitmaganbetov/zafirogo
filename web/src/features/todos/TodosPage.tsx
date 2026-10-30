import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { can, useSession } from '../../auth/session';
import { ConfirmForm } from '../../components/ConfirmForm';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { addDays, formatDate } from '../../lib/datetime';
import { setFlash, useFlash } from '../../lib/flash';
import { useInvalidateOn } from '../../realtime/hooks';
import { createTodo, deleteTodo, firstError, todosQuery, toggleTodo, type Todo } from './api';

/*
 * To Do List — перенос resources/views/todos/index.blade.php (TodoController::index()). Опрос /todos/state раз в
 * 15 с с location.reload() заменён событием todo.changed: список перечитывается сам (Q-062).
 */

const FIELD = 'rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';

/** d.m из Y-m-d (Carbon::parse($task->due_on)->format('d.m')). */
function dayMonth(date: string): string {
  const [, m, d] = date.split('-');
  return `${d}.${m}`;
}

export function TodosPage({ date }: { date?: string }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('To Do List'));
  useInvalidateOn(['todo.*'], ['todos']);
  const flash = useFlash('status');
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(flash);
  const { data } = useQuery(todosQuery(date));
  const tz = me?.club?.timezone ?? 'UTC';
  const readOnly = me?.read_only === true;
  const canManage = can(me, 'todos.create');
  const canDelete = can(me, 'todos.delete');
  const canToggle = can(me, 'todos.update') && !readOnly;

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['todos'] });
  const toggle = useMutation({ mutationFn: (task: Todo) => toggleTodo(task.id, !task.done), onSuccess: refresh, onError: (e) => setError(firstError(e)) });
  const remove = useMutation({
    mutationFn: (task: Todo) => deleteTodo(task.id),
    onSuccess: (r) => {
      setError(null);
      setStatus(r.message);
      return refresh();
    },
    onError: (e) => setError(firstError(e)),
  });

  const [title, setTitle] = useState('');
  const day = data?.date ?? date ?? '';
  const isPastOrToday = data !== undefined && (data.is_today || data.date < data.today);
  const [dueOn, setDueOn] = useState<string | null>(null);
  const dueValue = dueOn ?? (data ? (isPastOrToday ? data.today : data.date) : '');

  const create = useMutation({
    mutationFn: () => createTodo(title, dueValue),
    onSuccess: (r) => {
      setFlash('status', r.message);
      setTitle('');
      setDueOn(null);
      setError(null);
      if (r.date === data?.date) {
        setStatus(r.message);
        void refresh();
      } else {
        void navigate({ to: '/todos', search: { date: r.date } });
      }
    },
    onError: (e) => setError(firstError(e)),
  });

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    create.mutate();
  };

  const goto = (d: string) => void navigate({ to: '/todos', search: { date: d } });

  if (!data) return null;

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('To Do List')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('Один список на смену: если задачу отметил один сотрудник, у остальных она тоже выполнена.')}</p>
        </div>
        <form method="GET" action="/todos" className="flex items-center gap-2" onSubmit={(e) => e.preventDefault()}>
          <a href={`/todos?date=${addDays(day, -1)}`} className="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:text-gray-300" aria-label={t('Предыдущий день')}>
            <i className="fas fa-chevron-left"></i>
          </a>
          <input type="date" name="date" value={day} aria-label={t('День')} className={FIELD} onChange={(e) => e.target.value && goto(e.target.value)} />
          <a href={`/todos?date=${addDays(day, 1)}`} className="rounded-lg border border-gray-300 px-3 py-2 text-sm text-gray-700 dark:border-gray-700 dark:text-gray-300" aria-label={t('Следующий день')}>
            <i className="fas fa-chevron-right"></i>
          </a>
        </form>
      </header>

      {status && <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">{status}</div>}
      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {error}
        </div>
      )}

      {canManage && (
        <form method="POST" action="/todos" className="mb-6 flex flex-wrap items-end gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900" onSubmit={submit}>
          <label className="min-w-0 flex-1 basis-72">
            <span className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{t('Что нужно сделать')}</span>
            <input name="title" maxLength={200} required value={title} onChange={(e) => setTitle(e.target.value)} className={`${FIELD} w-full`} />
          </label>
          <label>
            <span className="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">{t('На какой день')}</span>
            <input type="date" name="due_on" required value={dueValue} onChange={(e) => setDueOn(e.target.value)} className={FIELD} />
          </label>
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm" disabled={readOnly || create.isPending}>
            <i className="fas fa-plus"></i>
            {t('Добавить задачу')}
          </button>
        </form>
      )}

      <ul className="divide-y divide-gray-100 overflow-hidden rounded-xl border border-gray-200 bg-white dark:divide-gray-800 dark:border-gray-800 dark:bg-gray-900" data-todo-list>
        {data.tasks.length === 0 && <li className="px-4 py-10 text-center text-sm text-gray-500 dark:text-gray-400">{t('На этот день задач нет.')}</li>}
        {data.tasks.map((task) => {
          const overdue = task.due_on < data.date;
          return (
            <li key={task.id} className="flex items-center gap-3 px-4 py-3" data-task={task.id}>
              <form
                method="POST"
                className="flex flex-1 items-center gap-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (canToggle) toggle.mutate(task);
                }}
              >
                <button
                  type="submit"
                  className={`grid h-6 w-6 flex-none place-items-center rounded-md border ${task.done ? 'border-green-600 bg-green-600 text-white' : 'border-gray-300 dark:border-gray-600'}`}
                  aria-pressed={task.done ? 'true' : 'false'}
                  aria-label={task.done ? t('Отметить как невыполненную') : t('Отметить как выполненную')}
                  data-toggle
                >
                  {task.done && <i className="fas fa-check text-xs"></i>}
                </button>
                <span className="min-w-0 flex-1">
                  <span className={`block text-sm ${task.done ? 'text-gray-400 line-through' : 'text-gray-900 dark:text-white'}`} data-title>
                    {task.title}
                  </span>
                  <span className="block text-xs text-gray-500 dark:text-gray-400" data-meta>
                    {overdue && <span className="font-medium text-red-600">{t('Просрочено с {{date}}', { date: dayMonth(task.due_on) })}</span>}
                    {overdue && task.done && ' '}
                    {task.done &&
                      t('Выполнил(а) {{name}} в {{time}}', { name: task.done_by ?? '—', time: task.done_at ? formatDate(task.done_at, tz, 'H:i') : '' })}
                  </span>
                </span>
              </form>
              {canDelete && (
                <ConfirmForm message={t('Удалить задачу?')} onConfirm={() => remove.mutate(task)}>
                  <button type="submit" className="text-gray-400 hover:text-red-600" aria-label={t('Удалить задачу')} disabled={readOnly}>
                    <i className="fas fa-trash"></i>
                  </button>
                </ConfirmForm>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
