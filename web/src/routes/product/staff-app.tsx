import { createFileRoute } from '@tanstack/react-router';
import { landingQuery } from '../../features/public/api';
import { publicSiteGuard } from '../../features/public/guard';
import { StaffAppPage } from '../../features/public/StaffAppPage';

// GET /product/staff-app product.staff-app → LandingController::staffApp() (routes/web.php:149): публичный.
export const Route = createFileRoute('/product/staff-app')({
  beforeLoad: publicSiteGuard,
  loader: ({ context }) => context.queryClient.ensureQueryData(landingQuery()),
  component: StaffAppPage,
});
