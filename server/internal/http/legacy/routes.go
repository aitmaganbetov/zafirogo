package legacy

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
)

// routes — маршруты, которые вызывают приложения (docs/mobile-compat.md §1). Middleware — в порядке Laravel:
// группа (auth:sanctum, current-club, subscription, club-role, throttle группы), затем маршрута (club-can, throttle).
func (a *API) routes() chi.Router {
	r := chi.NewRouter()
	r.NotFound(a.notFound)
	r.MethodNotAllowed(a.notFound)

	// routes/api.php: /api/auth/*
	r.Post("/api/auth/login", a.login)
	r.Post("/api/auth/password/code", a.passwordRoute(a.passwordCode))
	r.Post("/api/auth/password/reset", a.passwordRoute(a.passwordReset))
	r.Group(func(r chi.Router) {
		r.Use(a.authenticate)
		r.With(a.subscription).Get("/api/auth/me", a.me)
		r.Post("/api/auth/logout", a.logout)
		r.With(a.throttle("api.auth.clubs")).Get("/api/auth/clubs", a.clubs)
		r.With(a.throttle("api.auth.switch-club")).Post("/api/auth/switch-club", a.switchClub)
	})

	// routes/api.php: гостевые маршруты mobileclient (auth:sanctum, current-club, subscription).
	r.Route("/api/guest", func(r chi.Router) {
		r.Use(a.authenticate, a.subscription)
		r.With(a.throttle("api.guest.friends.index")).Get("/friends", a.friendsIndex)
		r.With(a.throttle("api.guest.friends.store")).Post("/friends", a.friendsStore)
		r.With(a.throttle("api.guest.friends.accept")).Post("/friends/{friendship}/accept", a.friendsAccept)
		r.With(a.throttle("api.guest.friends.remove")).Post("/friends/{friendship}/remove", a.friendsRemove)
		r.With(a.throttle("api.guest.friends.transfer.code")).Post("/friends/{friendship}/transfer/code", a.transferCode)
		r.With(a.throttle("api.guest.friends.transfer.confirm")).Post("/friends/{friendship}/transfer/confirm", a.transferConfirm)
		r.With(a.throttle("api.guest.news")).Get("/news", a.news)
		r.With(a.throttle("api.guest.history")).Get("/history", a.history)
		r.With(a.throttle("api.guest.qr-login.show")).Get("/qr-login/{code:[A-Za-z0-9]{40}}", a.qrShow)
		r.With(a.throttle("api.guest.qr-login.approve")).Post("/qr-login/{code:[A-Za-z0-9]{40}}", a.qrApprove)
	})

	// routes/api_staff.php (mobileemployee).
	r.Route("/api/staff", func(r chi.Router) {
		r.Use(a.authenticate, a.subscription, roles(permissions.StaffRoles), a.throttle(ratelimit.StaffApp))
		r.Get("/me", a.staffMe)

		r.Get("/computers", a.staffComputers)
		r.With(a.can("pc_control.update")).Post("/computers/control", a.control)
		r.With(a.can("cash.create"), a.throttle("api.staff.guest-session")).Post("/computers/guest-session", a.guestSession)
		r.With(roles(permissions.ComputerStaffRoles)).Post("/computers/{computer}/end-session", a.endSession)

		r.Route("/cash", func(r chi.Router) {
			r.Use(a.can("cash.view"))
			r.Get("/", a.cashShow)
			r.Group(func(r chi.Router) {
				r.Use(a.can("cash.create"))
				r.Post("/shifts", a.cashOpen)
				r.Post("/shifts/{shift}/close", a.cashClose)
				r.Post("/top-ups", a.cashTopUp)
				r.Post("/sales", a.cashSale)
				r.Post("/transactions", a.cashTransaction)
			})
		})

		r.Get("/customers", a.customers)
		r.Get("/products", a.products)
		r.With(a.can("stock.create")).Post("/stock/receive", a.stockMove(true))
		r.With(a.can("warehouse.update")).Post("/stock/movements", a.stockMove(false))

		r.Get("/orders", a.orders)
		r.With(a.can("orders.update")).Post("/orders/{order}/done", a.handleOrder(shop.HandOver))
		r.With(a.can("orders.delete")).Post("/orders/{order}/cancel", a.handleOrder(shop.Cancel))

		r.Get("/chats", a.chats)
		r.Get("/chat/poll", a.chatPoll)
		r.With(a.can("chat.create"), a.throttle("api.staff.chat.reply")).Post("/chat/{customer}", a.chatReply)

		// ClubNotifications::for() собирает раздел notifications нового REST; формат ответа тот же.
		r.Get("/notifications", a.passthrough(http.MethodGet, "/api/notifications"))
		r.Post("/notifications/seen", a.passthrough(http.MethodPost, "/api/notifications/seen"))
		r.With(roles(permissions.ReportRoles)).Get("/reports/today", a.reportToday)
	})
	return r
}

// notFound — маршрут старого контракта, который здесь не перенесён (его вызывает только агент старой версии):
// токен клуба этого сервера — 404 как у Laravel, чужой — на старый сервер.
func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := bearer(r); ok {
		a.authenticate(http.HandlerFunc(a.routeNotFound)).ServeHTTP(w, r)
		return
	}
	if a.forward(w, r) {
		return
	}
	a.routeNotFound(w, r)
}

// routeNotFound — NotFoundHttpException маршрута: «The route … could not be found.».
func (a *API) routeNotFound(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}
	writeMessage(w, http.StatusNotFound, "The route "+path+" could not be found.")
}
