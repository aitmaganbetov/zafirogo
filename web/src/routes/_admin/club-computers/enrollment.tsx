import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { enrollmentQuery } from '../../../features/computers/api';
import { EnrollmentPage } from '../../../features/computers/EnrollmentPage';

// GET /club-computers/enrollment — новый экран (Q-037): подключение ПК. Видят те, кто видит список ПК;
// действия — по ролям (сервис agent/enroll).
export const Route = createFileRoute('/_admin/club-computers/enrollment')({
  beforeLoad: requirePermission('club_map.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(enrollmentQuery()),
  component: EnrollmentPage,
});
