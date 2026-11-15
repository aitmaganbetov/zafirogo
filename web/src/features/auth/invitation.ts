import { queryOptions } from '@tanstack/react-query';
import { unwrap } from '../../api/http';
import { authApi } from '../../auth/session';

/** GET /api/invitations/{token} (ClubInvitationController::show()). */
export function invitationQuery(token: string) {
  return queryOptions({
    queryKey: ['invitation', token],
    queryFn: ({ signal }) => unwrap(authApi.GET('/api/invitations/{token}', { params: { path: { token } }, signal })),
    staleTime: 0,
  });
}
