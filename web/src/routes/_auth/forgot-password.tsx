import { createFileRoute } from '@tanstack/react-router';
import { ForgotPasswordPage } from '../../features/auth/ForgotPasswordPage';

// GET /forgot-password auth.password.forgot (routes/web.php:160).
export const Route = createFileRoute('/_auth/forgot-password')({
  component: ForgotPasswordPage,
});
