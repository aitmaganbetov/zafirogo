using Google.Protobuf;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Local.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Journal;

/// <summary>
/// События агента «как минимум один раз» (agent.proto, AgentEvent): событие пишется на диск до отправки и удаляется
/// по EventAck; после переподключения неподтверждённые отправляются снова (сервер дедуплицирует по event_id).
/// </summary>
public sealed class EventJournalStore
{
    /// <summary>Предел журнала: старые события вытесняются (ПК долго без связи).</summary>
    public const int Limit = 1000;

    private readonly object _gate = new();
    private readonly string? _path;
    private readonly EventJournal _journal;

    public EventJournalStore(string? path)
    {
        _path = path;
        _journal = Load(path) ?? new EventJournal();
    }

    /// <summary>Добавляет событие (сохраняется до отправки).</summary>
    public void Add(AgentEvent ev)
    {
        ArgumentNullException.ThrowIfNull(ev);
        lock (_gate)
        {
            _journal.Pending.Add(ev.Clone());
            while (_journal.Pending.Count > Limit) _journal.Pending.RemoveAt(0);
            Save();
        }
    }

    /// <summary>EventAck: событие доставлено.</summary>
    public bool Ack(string eventId)
    {
        lock (_gate)
        {
            for (var i = 0; i < _journal.Pending.Count; i++)
            {
                if (_journal.Pending[i].EventId != eventId) continue;
                _journal.Pending.RemoveAt(i);
                Save();
                return true;
            }

            return false;
        }
    }

    /// <summary>Неподтверждённые события по порядку.</summary>
    public IReadOnlyList<AgentEvent> Pending()
    {
        lock (_gate) return _journal.Pending.Select(e => e.Clone()).ToList();
    }

    private void Save()
    {
        if (_path is not null) AtomicFile.Write(_path, _journal.ToByteArray());
    }

    private static EventJournal? Load(string? path)
    {
        if (path is null) return null;
        var bytes = AtomicFile.Read(path);
        if (bytes is null) return null;
        try
        {
            return EventJournal.Parser.ParseFrom(bytes);
        }
        catch (InvalidProtocolBufferException)
        {
            return null;
        }
    }
}
