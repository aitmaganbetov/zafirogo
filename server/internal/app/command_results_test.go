package app

import (
	"testing"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
)

// Решение W-1: какие итоги команд агента становятся событием computer.command панели.
func TestCommandResultOf(t *testing.T) {
	id := identity.Identity{ClubID: 3, ComputerID: 7}
	res := func(st agentv1.CommandStatus, seq uint64, ec agentv1.ErrorCode) *agentv1.CommandResult {
		return &agentv1.CommandResult{CommandId: "c-1", Seq: seq, Status: st, ErrorCode: ec}
	}
	const (
		ok       = agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED
		failed   = agentv1.CommandStatus_COMMAND_STATUS_FAILED
		accepted = agentv1.CommandStatus_COMMAND_STATUS_ACCEPTED
		dup      = agentv1.CommandStatus_COMMAND_STATUS_DUPLICATE
		none     = agentv1.ErrorCode_ERROR_CODE_UNSPECIFIED
	)
	for _, tc := range []struct {
		name string
		id   identity.Identity
		kind commands.Kind
		res  *agentv1.CommandResult
		want *commandResultData
	}{
		{"перезагрузка выполнена", id, commands.KindReboot, res(ok, 5, none),
			&commandResultData{ComputerID: 7, CommandID: "c-1", Kind: "reboot", Status: "succeeded"}},
		{"сообщение не показано", id, commands.KindShowMessage, res(failed, 6, agentv1.ErrorCode_ERROR_CODE_COMMAND_UNSUPPORTED),
			&commandResultData{ComputerID: 7, CommandID: "c-1", Kind: "show_message", Status: "failed", ErrorCode: "command_unsupported"}},
		{"вид неизвестен этому инстансу, долговременная", id, "", res(ok, 9, none),
			&commandResultData{ComputerID: 7, CommandID: "c-1", Status: "succeeded"}},
		{"принята — ещё не итог", id, commands.KindReboot, res(accepted, 5, none), nil},
		{"повтор — итог уже был", id, commands.KindReboot, res(dup, 5, none), nil},
		{"сессия — не команда панели", id, commands.KindStartSession, res(ok, 5, none), nil},
		{"баланс — мгновенная служебная", id, commands.KindBalanceChanged, res(ok, 0, none), nil},
		{"мгновенная без вида", id, "", res(ok, 0, none), nil},
		{"нет клуба", identity.Identity{ComputerID: 7}, commands.KindReboot, res(ok, 5, none), nil},
		{"nil", id, commands.KindReboot, nil, nil},
	} {
		got, ok := commandResultOf(tc.id, tc.kind, tc.res)
		if (tc.want != nil) != ok || (ok && got != *tc.want) {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, ok, tc.want)
		}
	}
}
