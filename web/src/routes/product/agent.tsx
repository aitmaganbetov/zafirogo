import { createFileRoute } from '@tanstack/react-router';
import { landingQuery } from '../../features/public/api';
import { publicSiteGuard } from '../../features/public/guard';
import { AgentPage } from '../../features/public/AgentPage';

// GET /product/agent product.agent → LandingController::agent() (routes/web.php:147): публичный.
export const Route = createFileRoute('/product/agent')({
  beforeLoad: publicSiteGuard,
  loader: ({ context }) => context.queryClient.ensureQueryData(landingQuery()),
  component: AgentPage,
});
