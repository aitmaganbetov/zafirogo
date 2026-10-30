import { createFileRoute, redirect } from '@tanstack/react-router';

// GET / — лендинг (welcome.blade.php) переносит группа 7; пока — в панель (гостя гард отправит на /login).
export const Route = createFileRoute('/')({
  beforeLoad: () => {
    throw redirect({ to: '/dashboard' });
  },
});
