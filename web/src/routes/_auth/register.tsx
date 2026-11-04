import { createFileRoute } from '@tanstack/react-router';
import { RegisterPage } from '../../features/auth/RegisterPage';

// GET /register auth.show-register (routes/web.php:164).
export const Route = createFileRoute('/_auth/register')({
  component: RegisterPage,
});
