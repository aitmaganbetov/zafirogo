import { createFileRoute } from '@tanstack/react-router';
import { landingQuery } from '../features/public/api';
import { LandingPage } from '../features/public/LandingPage';

// GET / home → LandingController (routes/web.php:120): публичный, и для гостя, и для вошедшего.
export const Route = createFileRoute('/')({
  loader: ({ context }) => context.queryClient.ensureQueryData(landingQuery()),
  component: LandingPage,
});
