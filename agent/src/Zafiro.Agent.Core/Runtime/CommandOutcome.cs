using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Runtime;

/// <summary>Итог выполнения команды + действие после отправки CommandResult (перезагрузка, загрузка обновления).</summary>
public sealed record CommandOutcome(CommandStatus Status, ErrorCode Error = ErrorCode.Unspecified, string? Message = null, Func<Task>? After = null)
{
    public static CommandOutcome Ok(Func<Task>? after = null) => new(CommandStatus.Succeeded, After: after);

    public static CommandOutcome Fail(ErrorCode error, string? message = null) => new(CommandStatus.Failed, error, message);

    public static CommandOutcome Accepted(Func<Task> after) => new(CommandStatus.Accepted, After: after);
}
