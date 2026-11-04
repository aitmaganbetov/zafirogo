import { createFileRoute } from '@tanstack/react-router';
import { requireAuth } from '../../auth/guards';
import { PcLoginDonePage } from '../../features/pclogin/PcLoginPage';

// GET /pc-login/done pc-login.done (routes/web.php:150), middleware auth.
export const Route = createFileRoute('/pc-login/done')({
  beforeLoad: requireAuth,
  component: PcLoginDonePage,
});
