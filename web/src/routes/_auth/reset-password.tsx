import { createFileRoute, redirect } from '@tanstack/react-router';
import { recoveryPhone } from '../../features/auth/recovery';
import { ResetPasswordPage } from '../../features/auth/ResetPasswordPage';

// GET /reset-password auth.password.reset (routes/web.php:162): без телефона в сессии — назад на /forgot-password.
export const Route = createFileRoute('/_auth/reset-password')({
  beforeLoad: () => {
    if (recoveryPhone() === null) throw redirect({ to: '/forgot-password' });
  },
  component: ResetPasswordPage,
});
