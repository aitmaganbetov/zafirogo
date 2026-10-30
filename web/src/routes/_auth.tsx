import { Outlet, createFileRoute } from '@tanstack/react-router';
import { requireGuest } from '../auth/guards';

// Middleware `guest`: вход, регистрация, восстановление пароля — только для гостя (вошедшего — на /dashboard).
export const Route = createFileRoute('/_auth')({
  beforeLoad: requireGuest,
  component: Outlet,
});
