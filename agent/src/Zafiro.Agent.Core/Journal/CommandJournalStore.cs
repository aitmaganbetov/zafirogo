using Google.Protobuf;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Local.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Journal;

/// <summary>Решение по пришедшей команде.</summary>
public enum CommandCheck
{
    /// <summary>Новая команда — выполнить.</summary>
    Execute,
    /// <summary>Повтор: не выполнять, ответить сохранённым итогом (или DUPLICATE).</summary>
    Duplicate,
}

/// <summary>
/// Журнал команд сервера (agent.proto, шапка п. 5–6): last_command_seq и итоги последних 256 команд на диске
/// (атомарная запись до отправки CommandResult), чтобы после перезапуска службы или перезагрузки ПК (Reboot!) повтор
/// команды не выполнялся второй раз. Мгновенные команды (seq = 0) дедуплицируются по command_id в памяти.
/// </summary>
public sealed class CommandJournalStore
{
    /// <summary>Сколько итогов хранится (agent.proto п. 5).</summary>
    public const int RecentLimit = 256;

    private readonly object _gate = new();
    private readonly string? _path;
    private readonly CommandJournal _journal;
    private readonly LinkedList<string> _instantOrder = new();
    private readonly Dictionary<string, CommandResult> _instant = new(StringComparer.Ordinal);

    /// <param name="path">Файл журнала; null — только в памяти (тесты).</param>
    public CommandJournalStore(string? path)
    {
        _path = path;
        _journal = Load(path) ?? new CommandJournal();
    }

    /// <summary>seq последней выполненной долговременной команды (Hello.last_command_seq).</summary>
    public ulong LastCommandSeq
    {
        get
        {
            lock (_gate) return _journal.LastCommandSeq;
        }
    }

    /// <summary>Проверка повтора. Для повтора <paramref name="saved"/> — ответ (сохранённый итог или DUPLICATE).</summary>
    public CommandCheck Check(Command cmd, out CommandResult? saved)
    {
        ArgumentNullException.ThrowIfNull(cmd);
        lock (_gate)
        {
            saved = Find(cmd.CommandId)?.Clone();
            if (saved is not null) return CommandCheck.Duplicate;
            if (cmd.Seq > 0 && cmd.Seq <= _journal.LastCommandSeq)
            {
                saved = new CommandResult { CommandId = cmd.CommandId, Seq = cmd.Seq, Status = CommandStatus.Duplicate };
                return CommandCheck.Duplicate;
            }

            return CommandCheck.Execute;
        }
    }

    /// <summary>
    /// Записывает итог (до его отправки). Для долговременной команды продвигает last_command_seq и сохраняет журнал на
    /// диск атомарно; мгновенные — только в памяти.
    /// </summary>
    public void Record(CommandResult result)
    {
        ArgumentNullException.ThrowIfNull(result);
        lock (_gate)
        {
            if (result.Seq == 0)
            {
                if (_instant.ContainsKey(result.CommandId)) _instantOrder.Remove(result.CommandId);
                _instant[result.CommandId] = result.Clone();
                _instantOrder.AddLast(result.CommandId);
                while (_instantOrder.Count > RecentLimit)
                {
                    _instant.Remove(_instantOrder.First!.Value);
                    _instantOrder.RemoveFirst();
                }

                return;
            }

            for (var i = 0; i < _journal.Recent.Count; i++)
            {
                if (_journal.Recent[i].CommandId == result.CommandId)
                {
                    _journal.Recent.RemoveAt(i);
                    break;
                }
            }

            _journal.Recent.Add(result.Clone());
            while (_journal.Recent.Count > RecentLimit) _journal.Recent.RemoveAt(0);
            if (result.Seq > _journal.LastCommandSeq) _journal.LastCommandSeq = result.Seq;
            Save();
        }
    }

    /// <summary>Итог долгой команды, который ещё не доставлен (связи не было): сохраняется до <see cref="Delivered"/>.</summary>
    public void AddUndelivered(CommandResult result)
    {
        ArgumentNullException.ThrowIfNull(result);
        lock (_gate)
        {
            _journal.Undelivered.Add(result.Clone());
            while (_journal.Undelivered.Count > 32) _journal.Undelivered.RemoveAt(0);
            Save();
        }
    }

    /// <summary>Недоставленные итоги (досылаются после Welcome).</summary>
    public IReadOnlyList<CommandResult> Undelivered()
    {
        lock (_gate) return _journal.Undelivered.Select(r => r.Clone()).ToList();
    }

    /// <summary>Итог отправлен.</summary>
    public void Delivered(string commandId, CommandStatus status)
    {
        lock (_gate)
        {
            var removed = false;
            for (var i = _journal.Undelivered.Count - 1; i >= 0; i--)
            {
                if (_journal.Undelivered[i].CommandId == commandId && _journal.Undelivered[i].Status == status)
                {
                    _journal.Undelivered.RemoveAt(i);
                    removed = true;
                }
            }

            if (removed) Save();
        }
    }

    private CommandResult? Find(string commandId)
    {
        if (_instant.TryGetValue(commandId, out var r)) return r;
        for (var i = _journal.Recent.Count - 1; i >= 0; i--)
        {
            if (_journal.Recent[i].CommandId == commandId) return _journal.Recent[i];
        }

        return null;
    }

    private void Save()
    {
        if (_path is not null) AtomicFile.Write(_path, _journal.ToByteArray());
    }

    private static CommandJournal? Load(string? path)
    {
        if (path is null) return null;
        var bytes = AtomicFile.Read(path);
        if (bytes is null) return null;
        try
        {
            return CommandJournal.Parser.ParseFrom(bytes);
        }
        catch (InvalidProtocolBufferException)
        {
            // Испорченный журнал: начать с нуля. Сервер дошлёт команды с seq > 0 — повторное выполнение долговременных
            // команд безопасно по смыслу (StartSession/EndSession — состояние; питание — только неистёкшие, 120 с).
            return null;
        }
    }
}
