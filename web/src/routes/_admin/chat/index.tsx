import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { chatQuery, parseChatSearch } from '../../../features/chat/api';
import { ChatPage } from '../../../features/chat/ChatPage';

// GET /chat (routes/web.php:183, chat.index): club-area chat.view.
export const Route = createFileRoute('/_admin/chat/')({
  beforeLoad: requirePermission('chat.view'),
  validateSearch: parseChatSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(chatQuery(deps.customer)),
  component: ChatRoute,
});

function ChatRoute() {
  const { customer } = Route.useSearch();
  return <ChatPage key={customer ?? 0} customer={customer} />;
}
