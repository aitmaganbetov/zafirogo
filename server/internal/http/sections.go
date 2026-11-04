package httpserver

import (
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/activitylogs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/chat"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/computers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/dashboard"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/integrations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/layout"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/news"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/notifications"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/pclogin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/public"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/recovery"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/services"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/sessions"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/shoporders"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/staff"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/system"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/tariffs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/todos"
	v1 "github.com/aitmaganbetov/zafirogo/server/internal/http/sections/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/warehouse"
)

// Cookie сессии (их ставит раздел auth).
const (
	RefreshCookie = auth.RefreshCookie
	CSRFCookie    = auth.CSRFCookie
)

// Sections — реестр разделов REST: одна строка на раздел (api/<раздел>.yaml → sections/<раздел>).
// Порядок не важен; пути разделов не должны пересекаться. Конвенции — api/README.md.
func Sections() []httpx.Section {
	return []httpx.Section{
		system.Section(),
		auth.Section(),
		i18n.Section(),
		layout.Section(),
		cash.Section(),
		warehouse.Section(),
		customers.Section(),
		tariffs.Section(),
		sessions.Section(),
		staff.Section(),
		clubmap.Section(),
		dashboard.Section(),
		computers.Section(),
		services.Section(),
		shoporders.Section(),
		reservations.Section(),
		news.Section(),
		todos.Section(),
		chat.Section(),
		activitylogs.Section(),
		notifications.Section(),
		pclogin.Section(),
		recovery.Section(),
		public.Section(),
		billing.Section(),
		settings.Section(),
		integrations.Section(),
		superadmin.Section(),
		v1.Section(),
	}
}
