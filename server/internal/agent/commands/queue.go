package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ErrComputerNotFound — ПК нет в клубе.
var ErrComputerNotFound = errors.New("commands: computer not found")

// pendingPage — сколько команд досылать за один запрос.
const pendingPage = 200

// Queue — долговременные команды в Postgres.
type Queue struct {
	pool db.Beginner
}

// NewQueue создаёт очередь; pool — пул логина zafiro_app.
func NewQueue(pool db.Beginner) *Queue { return &Queue{pool: pool} }

func toUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func seqToInt(seq uint64) int64 {
	if seq > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(seq)
}

func seqFromInt(seq int64) uint64 {
	if seq < 0 {
		return 0
	}
	return uint64(seq)
}

// Enqueue выдаёт команде seq и сохраняет её (cmd.Seq заполняется). command_id, issued_at, expires_at
// уже заданы отправителем.
func (q *Queue) Enqueue(ctx context.Context, clubID, computerID int64, cmd *agentv1.Command) error {
	id, err := toUUID(cmd.GetCommandId())
	if err != nil {
		return fmt.Errorf("commands: command_id: %w", err)
	}
	var expires *time.Time
	if cmd.GetExpiresAt() != nil {
		t := cmd.GetExpiresAt().AsTime()
		expires = &t
	}
	var issuedBy *int64
	if v := cmd.GetIssuedByUserId(); v > 0 {
		issuedBy = &v
	}
	err = db.InTenantTx(ctx, q.pool, clubID, func(s *store.Queries) error {
		seq, err := s.NextAgentCommandSeq(ctx, store.NextAgentCommandSeqParams{ClubID: clubID, ComputerID: computerID})
		if err != nil {
			return err
		}
		cmd.Seq = seqFromInt(seq)
		payload, err := proto.Marshal(cmd)
		if err != nil {
			return err
		}
		return s.InsertAgentCommand(ctx, store.InsertAgentCommandParams{
			ClubID: clubID, ComputerID: computerID, Seq: seq, CommandID: id, Kind: string(KindOf(cmd)),
			Payload: payload, IssuedByUserID: issuedBy, CreatedAt: cmd.GetIssuedAt().AsTime(), ExpiresAt: expires,
		})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation: ПК нет в клубе
		return ErrComputerNotFound
	}
	if err != nil {
		return fmt.Errorf("commands: enqueue: %w", err)
	}
	return nil
}

// Pending — невыполненные неистёкшие команды с seq > afterSeq по возрастанию seq.
func (q *Queue) Pending(ctx context.Context, clubID, computerID int64, afterSeq uint64, now time.Time) ([]*agentv1.Command, error) {
	var out []*agentv1.Command
	after := seqToInt(afterSeq)
	for {
		var rows []store.ListPendingAgentCommandsRow
		err := db.InTenantTx(ctx, q.pool, clubID, func(s *store.Queries) error {
			var err error
			rows, err = s.ListPendingAgentCommands(ctx, store.ListPendingAgentCommandsParams{
				ClubID: clubID, ComputerID: computerID, AfterSeq: after, Now: &now, RowLimit: pendingPage})
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("commands: pending: %w", err)
		}
		for _, r := range rows {
			c := &agentv1.Command{}
			if err := proto.Unmarshal(r.Payload, c); err != nil {
				return nil, fmt.Errorf("commands: decode seq %d: %w", r.Seq, err)
			}
			c.Seq = seqFromInt(r.Seq)
			out = append(out, c)
			after = r.Seq
		}
		if len(rows) < pendingPage {
			return out, nil
		}
	}
}

// LatestSeq — последний выданный seq ПК (Welcome.latest_command_seq).
func (q *Queue) LatestSeq(ctx context.Context, clubID, computerID int64) (uint64, error) {
	var seq int64
	err := db.InTenantTx(ctx, q.pool, clubID, func(s *store.Queries) error {
		var err error
		seq, err = s.GetAgentCommandSeq(ctx, store.GetAgentCommandSeqParams{ClubID: clubID, ComputerID: computerID})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("commands: latest seq: %w", err)
	}
	return seqFromInt(seq), nil
}

// RaiseSeq — агент знает seq больше выданного (база восстановлена из копии): следующий seq будет больше.
func (q *Queue) RaiseSeq(ctx context.Context, clubID, computerID int64, seq uint64) error {
	return db.InTenantTx(ctx, q.pool, clubID, func(s *store.Queries) error {
		return s.RaiseAgentCommandSeq(ctx, store.RaiseAgentCommandSeqParams{ClubID: clubID, ComputerID: computerID, LastSeq: seqToInt(seq)})
	})
}

// StatusName — значение agent_commands.status для итога агента ("" — не итог и не принятие).
func StatusName(s agentv1.CommandStatus) string {
	switch s {
	case agentv1.CommandStatus_COMMAND_STATUS_ACCEPTED:
		return "accepted"
	case agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED:
		return "succeeded"
	case agentv1.CommandStatus_COMMAND_STATUS_FAILED:
		return "failed"
	case agentv1.CommandStatus_COMMAND_STATUS_DUPLICATE:
		return "duplicate"
	default:
		return ""
	}
}

// truncateRunes обрезает строку до n символов (result_message varchar(500)).
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// Record сохраняет ack/итог долговременной команды. updated = false — команды нет у этого ПК или итог уже есть.
func (q *Queue) Record(ctx context.Context, clubID, computerID int64, res *agentv1.CommandResult, now time.Time) (updated bool, err error) {
	id, err := toUUID(res.GetCommandId())
	if err != nil {
		return false, fmt.Errorf("commands: command_id: %w", err)
	}
	status := StatusName(res.GetStatus())
	if status == "" {
		return false, nil
	}
	var n int64
	err = db.InTenantTx(ctx, q.pool, clubID, func(s *store.Queries) error {
		var err error
		if status == "accepted" {
			n, err = s.AcceptAgentCommand(ctx, store.AcceptAgentCommandParams{Now: &now, ClubID: clubID, ComputerID: computerID, CommandID: id})
			return err
		}
		p := store.CompleteAgentCommandParams{Status: status, Now: &now, ClubID: clubID, ComputerID: computerID, CommandID: id}
		if c := int32(res.GetErrorCode()); c != 0 {
			p.ErrorCode = &c
		}
		if m := res.GetMessage(); m != "" {
			m = truncateRunes(m, 500)
			p.ResultMessage = &m
		}
		n, err = s.CompleteAgentCommand(ctx, p)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("commands: record result: %w", err)
	}
	return n > 0, nil
}
