import { createFileRoute } from '@tanstack/react-router';
import { landingQuery } from '../../features/public/api';
import { publicSiteGuard } from '../../features/public/guard';
import { FeaturesPage } from '../../features/public/FeaturesPage';

// GET /product/features product.features → LandingController::features() (routes/web.php:146): публичный.
export const Route = createFileRoute('/product/features')({
  beforeLoad: publicSiteGuard,
  loader: ({ context }) => context.queryClient.ensureQueryData(landingQuery()),
  component: FeaturesPage,
});
