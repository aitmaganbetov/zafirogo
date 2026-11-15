import { createFileRoute } from '@tanstack/react-router';
import { landingQuery, pricingQuery } from '../features/public/api';
import { publicSiteGuard } from '../features/public/guard';
import { PricingPage } from '../features/public/PricingPage';

// GET /pricing pricing → LandingController::pricing() (routes/web.php:150): публичный.
export const Route = createFileRoute('/pricing')({
  beforeLoad: publicSiteGuard,
  loader: ({ context }) =>
    Promise.all([context.queryClient.ensureQueryData(landingQuery()), context.queryClient.ensureQueryData(pricingQuery())]),
  component: PricingPage,
});
