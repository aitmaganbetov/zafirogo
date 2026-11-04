import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { parseTodosSearch, todosQuery } from '../../../features/todos/api';
import { TodosPage } from '../../../features/todos/TodosPage';

// GET /todos (routes/web.php:221, todos.index): club-area todos.view.
export const Route = createFileRoute('/_admin/todos/')({
  beforeLoad: requirePermission('todos.view'),
  validateSearch: parseTodosSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(todosQuery(deps.date)),
  component: TodosRoute,
});

function TodosRoute() {
  const { date } = Route.useSearch();
  return <TodosPage key={date ?? ''} date={date} />;
}
