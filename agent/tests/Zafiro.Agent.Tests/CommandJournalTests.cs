using Zafiro.Agent.Core.Journal;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

public class CommandJournalTests
{
    private static Command Cmd(ulong seq, string? id = null) => new() { CommandId = id ?? Guid.CreateVersion7().ToString(), Seq = seq, Lock = new V1.Lock() };

    private static CommandResult Ok(Command c) => new() { CommandId = c.CommandId, Seq = c.Seq, Status = CommandStatus.Succeeded };

    [Fact]
    public void Новая_команда_выполняется_повтор_command_id_отвечается_сохранённым_итогом()
    {
        var j = new CommandJournalStore(null);
        var c = Cmd(5);
        Assert.Equal(CommandCheck.Execute, j.Check(c, out _));
        j.Record(Ok(c));
        Assert.Equal(CommandCheck.Duplicate, j.Check(c, out var saved));
        Assert.Equal(CommandStatus.Succeeded, saved!.Status);
        Assert.Equal(c.CommandId, saved.CommandId);
        Assert.Equal(5UL, j.LastCommandSeq);
    }

    [Fact]
    public void Seq_не_больше_выполненного_без_итога_DUPLICATE()
    {
        var j = new CommandJournalStore(null);
        j.Record(Ok(Cmd(10)));
        Assert.Equal(CommandCheck.Duplicate, j.Check(Cmd(9), out var saved));
        Assert.Equal(CommandStatus.Duplicate, saved!.Status);
        Assert.Equal(9UL, saved.Seq);
        Assert.Equal(CommandCheck.Execute, j.Check(Cmd(11), out _));
    }

    [Fact]
    public void Мгновенные_дедуплицируются_в_памяти_и_не_двигают_seq()
    {
        var j = new CommandJournalStore(null);
        var ping = new Command { CommandId = Guid.CreateVersion7().ToString(), Ping = new Ping() };
        j.Record(Ok(ping));
        Assert.Equal(0UL, j.LastCommandSeq);
        Assert.Equal(CommandCheck.Duplicate, j.Check(ping, out _));
        Assert.Equal(CommandCheck.Execute, j.Check(new Command { CommandId = Guid.CreateVersion7().ToString(), Ping = new Ping() }, out _));
    }

    [Fact]
    public void Журнал_переживает_перезапуск_службы()
    {
        using var dir = new TempDir();
        var path = Path.Combine(dir.Path, "commands.pb");
        var c = Cmd(42);
        new CommandJournalStore(path).Record(Ok(c));

        var reloaded = new CommandJournalStore(path);
        Assert.Equal(42UL, reloaded.LastCommandSeq);
        Assert.Equal(CommandCheck.Duplicate, reloaded.Check(c, out var saved));
        Assert.Equal(CommandStatus.Succeeded, saved!.Status);
        if (!OperatingSystem.IsWindows())
            Assert.Equal(UnixFileMode.UserRead | UnixFileMode.UserWrite, File.GetUnixFileMode(path));
    }

    [Fact]
    public void Испорченный_журнал_начинается_с_нуля()
    {
        using var dir = new TempDir();
        var path = Path.Combine(dir.Path, "commands.pb");
        File.WriteAllBytes(path, [0xFF, 0xFF, 0xFF]);
        Assert.Equal(0UL, new CommandJournalStore(path).LastCommandSeq);
    }

    [Fact]
    public void Хранится_не_больше_256_итогов_seq_не_откатывается()
    {
        var j = new CommandJournalStore(null);
        var first = Cmd(1);
        j.Record(Ok(first));
        for (ulong s = 2; s <= 300; s++) j.Record(Ok(Cmd(s)));
        // Итог первой вытеснен, но seq 1 ≤ 300 — всё равно повтор.
        Assert.Equal(CommandCheck.Duplicate, j.Check(first, out var saved));
        Assert.Equal(CommandStatus.Duplicate, saved!.Status);
        Assert.Equal(300UL, j.LastCommandSeq);
    }

    [Fact]
    public void Недоставленные_итоги_хранятся_до_отправки()
    {
        using var dir = new TempDir();
        var path = Path.Combine(dir.Path, "commands.pb");
        var j = new CommandJournalStore(path);
        var r = new CommandResult { CommandId = Guid.CreateVersion7().ToString(), Seq = 3, Status = CommandStatus.Failed, ErrorCode = ErrorCode.UpdateVerificationFailed };
        j.AddUndelivered(r);
        Assert.Single(new CommandJournalStore(path).Undelivered());
        j.Delivered(r.CommandId, r.Status);
        Assert.Empty(new CommandJournalStore(path).Undelivered());
    }

    [Fact]
    public void События_до_EventAck_на_диске()
    {
        using var dir = new TempDir();
        var path = Path.Combine(dir.Path, "events.pb");
        var j = new EventJournalStore(path);
        var a = new AgentEvent { EventId = Guid.CreateVersion7().ToString(), GuestSignedOut = new GuestSignedOut { UserId = 5 } };
        var b = new AgentEvent { EventId = Guid.CreateVersion7().ToString(), ServiceModeChanged = new ServiceModeChanged { Active = true } };
        j.Add(a);
        j.Add(b);
        Assert.Equal([a.EventId, b.EventId], new EventJournalStore(path).Pending().Select(e => e.EventId));
        Assert.True(j.Ack(a.EventId));
        Assert.False(j.Ack(a.EventId));
        Assert.Equal([b.EventId], new EventJournalStore(path).Pending().Select(e => e.EventId));
    }
}
