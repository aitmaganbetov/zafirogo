import { createFileRoute } from '@tanstack/react-router';
import { InvitationPage } from '../../features/auth/InvitationPage';
import { invitationQuery } from '../../features/auth/invitation';

// GET /invitations/{token} club-invitations.show (routes/web.php:145): и для гостя, и для вошедшего.
export const Route = createFileRoute('/invitations/$token')({
  loader: ({ context, params }) => context.queryClient.ensureQueryData(invitationQuery(params.token)),
  component: InvitationRoute,
});

function InvitationRoute() {
  const { token } = Route.useParams();
  return <InvitationPage token={token} />;
}
