using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>Выполнение команд сервера без сервера: дедупликация, сроки, состояние ПК, отказы.</summary>
public sealed class RuntimeCommandTests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();
    private readonly FakePlatform _platform = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private AgentRuntime Runtime() => Runtimes.Create(_dir.Path, _platform, _pki);

    private static Command Cmd(ulong seq, Action<Command> kind, DateTimeOffset? expires = null)
    {
        var c = new Command { CommandId = Guid.CreateVersion7().ToString(), Seq = seq, IssuedAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow) };
        if (expires is { } e) c.ExpiresAt = Timestamp.FromDateTimeOffset(e);
        kind(c);
        return c;
    }

    [Fact]
    public async Task Повтор_перезагрузки_не_перезагружает_второй_раз()
    {
        await using var rt = Runtime();
        var reboot = Cmd(1, c => c.Reboot = new Reboot());
        var first = await rt.HandleCommandAsync(reboot, CancellationToken.None);
        Assert.Equal(CommandStatus.Succeeded, first.Status);
        await WaitFor(() => _platform.Reboots == 1);

        var again = await rt.HandleCommandAsync(reboot, CancellationToken.None);
        Assert.Equal(CommandStatus.Succeeded, again.Status); // сохранённый итог
        await Task.Delay(200);
        Assert.Equal(1, _platform.Reboots);
        Assert.Equal(1UL, rt.Commands.LastCommandSeq);
    }

    [Fact]
    public async Task После_перезапуска_службы_повтор_по_seq_не_выполняется()
    {
        var reboot = Cmd(7, c => c.Reboot = new Reboot());
        await using (var rt = Runtime()) await rt.HandleCommandAsync(reboot, CancellationToken.None);
        await WaitFor(() => _platform.Reboots == 1);

        await using var restarted = Runtime();
        Assert.Equal(7UL, restarted.Commands.LastCommandSeq);
        var dup = await restarted.HandleCommandAsync(Cmd(7, c => c.Reboot = new Reboot()), CancellationToken.None);
        Assert.Equal(CommandStatus.Duplicate, dup.Status);
        await Task.Delay(200);
        Assert.Equal(1, _platform.Reboots);
    }

    [Fact]
    public async Task Истёкшая_команда_не_выполняется()
    {
        await using var rt = Runtime();
        var r = await rt.HandleCommandAsync(Cmd(3, c => c.Shutdown = new Shutdown(), DateTimeOffset.UtcNow.AddSeconds(-1)), CancellationToken.None);
        Assert.Equal(CommandStatus.Failed, r.Status);
        Assert.Equal(ErrorCode.CommandExpired, r.ErrorCode);
        await Task.Delay(100);
        Assert.Equal(0, _platform.Shutdowns);
        Assert.Equal(3UL, rt.Commands.LastCommandSeq);
    }

    [Fact]
    public async Task Сессия_начинается_и_завершается_по_командам()
    {
        await using var rt = Runtime();
        var session = new ActiveSession { SessionId = "s-1", UserId = 0, Guest = true, EndsAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddHours(1)) };
        Assert.Equal(CommandStatus.Succeeded, (await rt.HandleCommandAsync(Cmd(1, c => c.StartSession = new StartSession { Session = session }), CancellationToken.None)).Status);
        Assert.Equal(ComputerState.InSession, rt.State.ComputerState);
        Assert.Equal(Screen.Setup, rt.State.Screen); // не зарегистрирован — экран регистрации
        rt.State.SetConnection(Connection.Online);
        Assert.Equal(Screen.None, rt.State.Screen); // идёт сессия — UI не нужен

        var mismatch = await rt.HandleCommandAsync(Cmd(2, c => c.EndSession = new EndSession { SessionId = "other" }), CancellationToken.None);
        Assert.Equal(ErrorCode.SessionMismatch, mismatch.ErrorCode);
        Assert.Equal(ComputerState.InSession, rt.State.ComputerState);

        Assert.Equal(CommandStatus.Succeeded, (await rt.HandleCommandAsync(Cmd(3, c => c.EndSession = new EndSession { SessionId = "s-1" }), CancellationToken.None)).Status);
        Assert.Equal(ComputerState.Idle, rt.State.ComputerState);
    }

    [Fact]
    public async Task Unlock_без_сессии_и_блокировки_отклоняется()
    {
        await using var rt = Runtime();
        var r = await rt.HandleCommandAsync(Cmd(1, c => c.Unlock = new Unlock()), CancellationToken.None);
        Assert.Equal(ErrorCode.CommandRejected, r.ErrorCode);
        await rt.HandleCommandAsync(Cmd(2, c => c.Lock = new V1.Lock { Reason = "Техобслуживание" }), CancellationToken.None);
        Assert.Equal("Техобслуживание", rt.State.LockReason);
        Assert.Equal(CommandStatus.Succeeded, (await rt.HandleCommandAsync(Cmd(3, c => c.Unlock = new Unlock()), CancellationToken.None)).Status);
    }

    [Fact]
    public async Task Сообщение_сотрудника_уходит_в_UI_и_обрезается_до_300()
    {
        await using var rt = Runtime();
        ShowMessage? shown = null;
        rt.UiEvent += e =>
        {
            if (e.KindCase == Event.KindOneofCase.ShowMessage) shown = e.ShowMessage;
        };
        await rt.HandleCommandAsync(Cmd(1, c => c.ShowMessage = new ShowMessage { Text = new string('я', 400) }), CancellationToken.None);
        Assert.NotNull(shown);
        Assert.Equal(300, shown!.Text.Length);
    }

    [Fact]
    public async Task Обновление_с_неверной_подписью_не_скачивается()
    {
        await using var rt = Runtime();
        var update = new UpdateAvailable
        {
            Version = "9.9.9", Url = "https://github.com/x.exe", Sha256 = new string('a', 64), SizeBytes = 10,
            SignatureEd25519 = ByteString.CopyFrom(new byte[64]), KeyId = "unknown", Channel = "stable",
        };
        var r = await rt.HandleCommandAsync(Cmd(1, c => c.UpdateAvailable = update), CancellationToken.None);
        // Установщика (UpdateInstaller) в тесте нет: без него — UNSUPPORTED; с ним и без ключа — UPDATE_VERIFICATION_FAILED.
        Assert.Equal(CommandStatus.Failed, r.Status);
        Assert.Contains(r.ErrorCode, new[] { ErrorCode.CommandUnsupported, ErrorCode.UpdateVerificationFailed });
        Assert.Equal(0, _platform.Installs);
    }

    [Fact]
    public async Task Ping_синхронизирует_часы_и_возвращает_время_агента()
    {
        await using var rt = Runtime();
        var serverTime = DateTimeOffset.UtcNow.AddMinutes(10);
        var r = await rt.HandleCommandAsync(Cmd(0, c => c.Ping = new Ping { ServerTime = Timestamp.FromDateTimeOffset(serverTime) }), CancellationToken.None);
        Assert.NotNull(r.AgentTime);
        Assert.InRange(rt.State.ServerNow(), serverTime.AddSeconds(-5), serverTime.AddSeconds(5));
    }

    [Fact]
    public async Task Техрежим_и_события_на_диске()
    {
        await using var rt = Runtime();
        rt.State.SetConnection(Connection.Online);
        Assert.Equal(Screen.Lock, rt.State.Screen);
        await rt.HandleCommandAsync(Cmd(1, c => c.EnterServiceMode = new EnterServiceMode { StaffMinutes = 5 }), CancellationToken.None);
        Assert.Equal(ComputerState.Service, rt.State.ComputerState);
        Assert.Equal(Screen.None, rt.State.Screen);
        var ev = Assert.Single(rt.Events.Pending());
        Assert.True(ev.ServiceModeChanged.Active);
    }

    [Fact]
    public async Task Wake_on_lan_не_больше_50_адресов()
    {
        await using var rt = Runtime();
        var wol = new WakeOnLan();
        for (var i = 0; i < 60; i++) wol.MacAddresses.Add(FakePlatform.RandomMac());
        await rt.HandleCommandAsync(Cmd(1, c => c.WakeOnLan = wol), CancellationToken.None);
        Assert.Equal(50, _platform.Woken.Count);
    }

    [Fact]
    public async Task Неизвестная_команда_UNSUPPORTED()
    {
        await using var rt = Runtime();
        var r = await rt.HandleCommandAsync(new Command { CommandId = Guid.CreateVersion7().ToString(), Seq = 1 }, CancellationToken.None);
        Assert.Equal(ErrorCode.CommandUnsupported, r.ErrorCode);
    }

    private static async Task WaitFor(Func<bool> condition)
    {
        for (var i = 0; i < 100 && !condition(); i++) await Task.Delay(20);
        Assert.True(condition());
    }
}
