import { createFileRoute, notFound } from '@tanstack/react-router';
import { requireAuth } from '../../auth/guards';
import { PC_LOGIN_CODE, pcLoginQuery } from '../../features/pclogin/api';
import { PcLoginPage } from '../../features/pclogin/PcLoginPage';

// GET /pc-login/{code} pc-login.show (routes/web.php:151): middleware auth (без current-club — любой клуб кода);
// гостя — на /login, после входа (pc_login: true) — обратно сюда.
export const Route = createFileRoute('/pc-login/$code')({
  beforeLoad: async (args) => {
    if (!PC_LOGIN_CODE.test(args.params.code)) throw notFound();
    await requireAuth(args);
  },
  loader: ({ context, params }) => context.queryClient.ensureQueryData(pcLoginQuery(params.code)),
  component: PcLoginRoute,
});

function PcLoginRoute() {
  const { code } = Route.useParams();
  return <PcLoginPage code={code} />;
}
