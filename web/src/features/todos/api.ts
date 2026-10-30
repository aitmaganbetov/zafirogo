import { queryOptions } from '@tanstack/react-query';
import { createApiClient, isApiError, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/todos';

/*
 * To Do List (api/todos.yaml). Laravel: TodoController. Ключи Query — ['todos', …]; обновления — событие
 * todo.changed (WebSocket) вместо опроса /todos/state раз в 15 с.
 */

export const todosApi = createApiClient<paths>();

type S = components['schemas'];
export type Todo = S['Todo'];
export type TodoDay = S['TodoDay'];

export interface TodosSearch {
  readonly date?: string;
}

export function parseTodosSearch(s: Record<string, unknown>): TodosSearch {
  return { date: typeof s.date === 'string' && s.date !== '' ? s.date : undefined };
}

export const todosQuery = (date?: string) =>
  queryOptions({
    queryKey: ['todos', date ?? ''],
    queryFn: () => unwrap(todosApi.GET('/api/todos', { params: { query: { date } } })),
  });

export function createTodo(title: string, dueOn: string) {
  return unwrap(todosApi.POST('/api/todos', { body: { title, due_on: dueOn } }));
}

export function toggleTodo(id: number, done: boolean) {
  return unwrap(todosApi.POST('/api/todos/{todoId}/toggle', { params: { path: { todoId: id } }, body: { done } }));
}

export function deleteTodo(id: number) {
  return unwrap(todosApi.DELETE('/api/todos/{todoId}', { params: { path: { todoId: id } } }));
}

/** $errors->first(): первая ошибка ответа (сервер уже перевёл текст на язык клуба). */
export function firstError(e: unknown): string {
  if (isApiError(e)) {
    const first = Object.values(e.fieldErrors)[0]?.[0];
    return first ?? e.message;
  }
  return e instanceof Error ? e.message : String(e);
}
