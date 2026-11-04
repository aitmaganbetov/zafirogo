import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { UserPasswordPage } from '../../../features/customers/UserPasswordPage';

// Ответ POST /users/{membership}/password (users.password, customers.update): новый пароль показывается один раз.
export const Route = createFileRoute('/_admin/users/$id/password')({
  beforeLoad: requirePermission('customers.update'),
  component: UserPasswordRoute,
});

function UserPasswordRoute() {
  const { id } = Route.useParams();
  return <UserPasswordPage userId={Number(id)} />;
}
