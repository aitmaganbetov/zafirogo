-- To Do List клуба (Этап 3, группа 4). Laravel: app/Http/Controllers/TodoController.php, app/Models/TodoTask.php.

-- name: ListDayTodos :many
-- tasks(): задачи дня; на сегодня — плюс невыполненные просроченные; orderBy('due_on')->orderBy('id')->limit(300).
SELECT t.id, t.title, t.due_on, t.done_at, t.done_by, u.name AS done_by_name
FROM todo_tasks t
LEFT JOIN users u ON u.id = t.done_by
WHERE t.club_id = sqlc.arg(club_id)
  AND (t.due_on = sqlc.arg(day)::date
       OR (sqlc.arg(with_overdue)::boolean AND t.due_on < sqlc.arg(day)::date AND t.done_at IS NULL))
ORDER BY t.due_on, t.id
LIMIT 300;

-- name: GetTodo :one
-- Задача клуба с исполнителем (ответ после изменения).
SELECT t.id, t.title, t.due_on, t.done_at, t.done_by, u.name AS done_by_name
FROM todo_tasks t
LEFT JOIN users u ON u.id = t.done_by
WHERE t.club_id = sqlc.arg(club_id) AND t.id = sqlc.arg(id);

-- name: InsertTodo :one
INSERT INTO todo_tasks (club_id, title, due_on, created_by, created_at, updated_at)
VALUES (sqlc.arg(club_id), sqlc.arg(title), sqlc.arg(due_on), sqlc.arg(created_by), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: LockTodo :one
-- TodoTask::lockForUpdate()->findOrFail().
SELECT id, title, due_on, done_at
FROM todo_tasks
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id)
FOR UPDATE;

-- name: SetTodoDone :exec
-- forceFill(['done_at' => $done ? now() : null, 'done_by' => $done ? user : null]).
UPDATE todo_tasks SET done_at = sqlc.narg(done_at), done_by = sqlc.narg(done_by), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);

-- name: DeleteTodo :exec
DELETE FROM todo_tasks WHERE club_id = sqlc.arg(club_id) AND id = sqlc.arg(id);
