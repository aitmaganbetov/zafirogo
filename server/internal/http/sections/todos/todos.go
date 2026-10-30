// Package todos — REST-раздел: To Do List клуба (api/todos.yaml). Конвенции — api/README.md.
//
// Laravel: app/Http/Controllers/TodoController.php. Один список на смену: задачу, отмеченную одним сотрудником,
// видят выполненной все; невыполненные задачи прошлых дней остаются в списке на сегодня как просроченные.
// Открытые страницы обновляются событием todo.changed (вместо опроса /todos/state раз в 15 с).
package todos

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	todosapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/todos"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var access = httpx.Access{
	Routes: map[string]string{
		"ListTodos":  "todos.index",
		"CreateTodo": "todos.store",
		"ToggleTodo": "todos.toggle",
		"DeleteTodo": "todos.destroy",
	},
}

type handlers struct {
	d *httpx.Deps
}

var _ todosapi.StrictServerInterface = (*handlers)(nil)

// Section — To Do List. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "todos", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := todosapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[todosapi.StrictMiddlewareFunc](access),
		todosapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	todosapi.HandlerWithOptions(si, todosapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// Тексты TodoController (ключи перевода).
const (
	msgAdded      = "Задача добавлена."
	msgDeleted    = "Задача удалена."
	msgPastDay    = "Задачу нельзя поставить на прошедший день."
	logCreated    = "Задача на :date: :title"
	logDone       = "Выполнена задача: :title"
	logReopened   = "Задача снова не выполнена: :title"
	logDeleted    = "Удалена задача: :title"
	subjectTodo   = `App\Models\TodoTask`
	dayLayout     = "2006-01-02"
	maxTitleChars = 200
)

func todoView(id int64, title string, dueOn time.Time, doneAt *time.Time, doneByName *string) todosapi.Todo {
	return todosapi.Todo{Id: id, Title: title, DueOn: dueOn.Format(dayLayout), Done: doneAt != nil, DoneAt: doneAt, DoneBy: doneByName}
}

// day — день из ?date= (date_format:Y-m-d) или сегодня клуба.
func day(info club.Info, date *string, now time.Time) (time.Time, error) {
	if date == nil || *date == "" {
		return info.StartOfDay(now), nil
	}
	d, ok := info.ParseDay(*date)
	if !ok {
		return time.Time{}, httpx.RuleErrorWith("date", "date_format", "Y-m-d")
	}
	return d, nil
}

// ListTodos — index()/state(): задачи дня; на сегодня — плюс невыполненные просроченные.
func (h *handlers) ListTodos(ctx context.Context, req todosapi.ListTodosRequestObject) (todosapi.ListTodosResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	out := todosapi.TodoDay{Tasks: []todosapi.Todo{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		d, err := day(info, req.Params.Date, now)
		if err != nil {
			return err
		}
		today := info.StartOfDay(now)
		out.Date, out.Today = d.Format(dayLayout), today.Format(dayLayout)
		out.IsToday = out.Date == out.Today
		rows, err := q.ListDayTodos(ctx, store.ListDayTodosParams{ClubID: p.ClubID, Day: d, WithOverdue: out.IsToday})
		for _, r := range rows {
			out.Tasks = append(out.Tasks, todoView(r.ID, r.Title, r.DueOn, r.DoneAt, r.DoneByName))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return todosapi.ListTodos200JSONResponse(out), nil
}

// CreateTodo — store(): title required|max:200, due_on required|date_format:Y-m-d|after_or_equal:сегодня.
func (h *handlers) CreateTodo(ctx context.Context, req todosapi.CreateTodoRequestObject) (todosapi.CreateTodoResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("title", "required")
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	title := strings.TrimSpace(req.Body.Title) // TrimStrings
	var dueOn time.Time
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		var fe httpx.FormErrors
		switch n := utf8.RuneCountInString(title); {
		case n == 0:
			fe.Add("title", httpx.RuleMessage("title", "required", ""))
		case n > maxTitleChars:
			fe.Add("title", httpx.RuleMessage("title", "max_string", "200"))
		}
		if req.Body.DueOn == "" {
			fe.Add("due_on", httpx.RuleMessage("due_on", "required", ""))
		} else if d, ok := info.ParseDay(req.Body.DueOn); !ok {
			fe.Add("due_on", httpx.RuleMessage("due_on", "date_format", ""))
		} else if d.Before(info.StartOfDay(now)) {
			fe.Add("due_on", msgPastDay)
		} else {
			dueOn = d
		}
		if err := fe.Err(); err != nil {
			return err
		}
		id, err := q.InsertTodo(ctx, store.InsertTodoParams{ClubID: p.ClubID, Title: title, DueOn: dueOn, CreatedBy: &p.UserID, Now: &now})
		if err != nil {
			return err
		}
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "todo.created", UserID: p.UserID, SubjectType: subjectTodo, SubjectID: id, At: now,
		}.Translated(logCreated, map[string]string{"date": dueOn.Format("02.01.2006"), "title": title})); err != nil {
			return err
		}
		return changed(ctx, q, p.ClubID, id, "created", dueOn)
	})
	if err != nil {
		return nil, err
	}
	return todosapi.CreateTodo201JSONResponse{Message: h.d.T(ctx, msgAdded, nil), Date: dueOn.Format(dayLayout)}, nil
}

// ToggleTodo — toggle(): без изменения ничего не пишется; иначе done_at/done_by, журнал todo.done/todo.reopened.
func (h *handlers) ToggleTodo(ctx context.Context, req todosapi.ToggleTodoRequestObject) (todosapi.ToggleTodoResponseObject, error) {
	done := req.Body != nil && req.Body.Done
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var out todosapi.Todo
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		t, err := q.LockTodo(ctx, store.LockTodoParams{ClubID: p.ClubID, ID: req.TodoId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if (t.DoneAt != nil) != done {
			params := store.SetTodoDoneParams{ClubID: p.ClubID, ID: t.ID, Now: &now}
			event, key, action := "todo.reopened", logReopened, "reopened"
			if done {
				params.DoneAt, params.DoneBy = &now, &p.UserID
				event, key, action = "todo.done", logDone, "done"
			}
			if err := q.SetTodoDone(ctx, params); err != nil {
				return err
			}
			if _, err := activity.Record(ctx, q, activity.Entry{
				ClubID: p.ClubID, Event: event, UserID: p.UserID, SubjectType: subjectTodo, SubjectID: t.ID, At: now,
			}.Translated(key, map[string]string{"title": t.Title})); err != nil {
				return err
			}
			if err := changed(ctx, q, p.ClubID, t.ID, action, t.DueOn); err != nil {
				return err
			}
		}
		row, err := q.GetTodo(ctx, store.GetTodoParams{ClubID: p.ClubID, ID: t.ID})
		out = todoView(row.ID, row.Title, row.DueOn, row.DoneAt, row.DoneByName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return todosapi.ToggleTodo200JSONResponse(out), nil
}

// DeleteTodo — destroy(): журнал todo.deleted (без предмета — задачи больше нет).
func (h *handlers) DeleteTodo(ctx context.Context, req todosapi.DeleteTodoRequestObject) (todosapi.DeleteTodoResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		t, err := q.LockTodo(ctx, store.LockTodoParams{ClubID: p.ClubID, ID: req.TodoId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.DeleteTodo(ctx, store.DeleteTodoParams{ClubID: p.ClubID, ID: t.ID}); err != nil {
			return err
		}
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "todo.deleted", UserID: p.UserID, At: now,
		}.Translated(logDeleted, map[string]string{"title": t.Title})); err != nil {
			return err
		}
		return changed(ctx, q, p.ClubID, t.ID, "deleted", t.DueOn)
	})
	if err != nil {
		return nil, err
	}
	return todosapi.DeleteTodo200JSONResponse{Message: h.d.T(ctx, msgDeleted, nil)}, nil
}

// changed — todo.changed {todo_id, action, due_on} (docs/realtime.md).
func changed(ctx context.Context, q *store.Queries, clubID, id int64, action string, dueOn time.Time) error {
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "todo.changed", Data: map[string]any{
		"todo_id": id, "action": action, "due_on": dueOn.Format(dayLayout),
	}})
	return err
}
