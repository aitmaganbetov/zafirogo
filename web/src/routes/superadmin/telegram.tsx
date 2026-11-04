import { createFileRoute } from '@tanstack/react-router';
import { telegramQuery } from '../../features/superadmin/api';
import { TelegramPage } from '../../features/superadmin/TelegramPage';

// GET /telegram — superadmin.telegram.show (routes/web.php:85).
export const Route = createFileRoute('/superadmin/telegram')({
  loader: ({ context }) => context.queryClient.ensureQueryData(telegramQuery()),
  component: TelegramPage,
});
