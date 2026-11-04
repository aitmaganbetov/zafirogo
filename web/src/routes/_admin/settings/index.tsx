import { createFileRoute, redirect } from '@tanstack/react-router';

// GET /settings → redirect()->route('settings.club') (routes/web.php:207).
export const Route = createFileRoute('/_admin/settings/')({
  beforeLoad: () => {
    throw redirect({ to: '/settings/club' });
  },
});
