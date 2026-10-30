package httpserver

import (
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/customers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/layout"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/sessions"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/system"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/sections/tariffs"
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
		clubmap.Section(),
	}
}
