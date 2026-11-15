import { createFileRoute } from '@tanstack/react-router';
import { landingQuery } from '../../features/public/api';
import { publicSiteGuard } from '../../features/public/guard';
import { GuestAppPage } from '../../features/public/GuestAppPage';

// GET /product/guest-app product.guest-app → LandingController::guestApp() (routes/web.php:148): публичный.
export const Route = createFileRoute('/product/guest-app')({
  beforeLoad: publicSiteGuard,
  loader: ({ context }) => context.queryClient.ensureQueryData(landingQuery()),
  component: GuestAppPage,
});
