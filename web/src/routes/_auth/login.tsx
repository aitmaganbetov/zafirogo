import { createFileRoute } from '@tanstack/react-router';
import { LoginPage } from '../../features/auth/LoginPage';

// GET /login auth.show-login (routes/web.php:157).
export const Route = createFileRoute('/_auth/login')({
  component: LoginPage,
});
