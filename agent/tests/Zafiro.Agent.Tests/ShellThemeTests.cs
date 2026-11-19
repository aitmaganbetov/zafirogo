using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.UI.Screens;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>
/// Тема клуба на ПК (коммит старого e38b764, HeartbeatTests.DeviceHeartbeat_ReadsTheClubShellTheme): цвета из
/// ClubConfig.shell_theme доходят до UI только в формате #rrggbb, меняются push-ом ConfigChanged и переживают перезагрузку;
/// ярлык «Zafiro» выводит открытое окно вперёд (коммит 71db8d6).
/// </summary>
public sealed class ShellThemeTests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private static ShellTheme Red() => new()
    {
        Accent = "#DC2626", Accent2 = "#fb7185", Soft = "#fca5a5", Highlight = "#fda4af", Bg0 = "#0a0304", Bg1 = "#1f0709",
    };

    private static Command ConfigChanged(ClubConfig config) => new()
    {
        CommandId = Guid.CreateVersion7().ToString(),
        IssuedAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow),
        ConfigChanged = new ConfigChanged { Config = config },
    };

    [Fact]
    public void Тема_проверяется_как_у_старого_агента()
    {
        var red = ShellThemes.Normalize(Red());
        Assert.NotNull(red);
        Assert.Equal("#dc2626", red.Accent); // нижний регистр
        Assert.Equal("#1f0709", red.Bg1);

        Assert.Null(ShellThemes.Normalize(null)); // стандартная
        // Цвет не в формате #RRGGBB — стандартная тема (в CSS страницы не попадает).
        foreach (var broken in new Action<ShellTheme>[]
                 {
                     t => t.Accent = "red;background:url(x)",
                     t => t.Accent2 = "",
                     t => t.Soft = "#fca5a",
                     t => t.Highlight = "#fda4af ",
                     t => t.Bg0 = "0a0304a",
                     t => t.Bg1 = "#1g0709",
                 })
        {
            var theme = Red();
            broken(theme);
            Assert.Null(ShellThemes.Normalize(theme));
        }

        Assert.True(ShellThemes.Same(null, null));
        Assert.False(ShellThemes.Same(red, null));
        Assert.True(ShellThemes.Same(red, ShellThemes.Normalize(Red())));
    }

    [Fact]
    public async Task Тема_приходит_в_Welcome_и_меняется_ConfigChanged_без_перезапуска()
    {
        await using var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        var snapshots = new List<Snapshot>();
        rt.UiEvent += e =>
        {
            if (e.Snapshot is not null) snapshots.Add(e.Snapshot);
        };

        rt.State.ApplyWelcome(new Welcome { Config = new ClubConfig { ClubName = "Asia", ShellTheme = Red() } });
        var snap = rt.State.BuildSnapshot();
        Assert.Equal("#dc2626", snap.ShellTheme.Accent);
        Assert.Equal("#dc2626", snap.Config.ShellTheme.Accent);

        // Смена темы в настройках клуба — push ConfigChanged, UI получает новый снимок.
        var green = new ShellTheme { Accent = "#16a34a", Accent2 = "#4ade80", Soft = "#86efac", Highlight = "#bef264", Bg0 = "#030805", Bg1 = "#061a0e" };
        var r = await rt.HandleCommandAsync(ConfigChanged(new ClubConfig { ClubName = "Asia", ShellTheme = green }), CancellationToken.None);
        Assert.Equal(CommandStatus.Succeeded, r.Status);
        Assert.Equal("#16a34a", snapshots[^1].ShellTheme.Accent);

        // Неверный цвет — стандартная тема и в снимке, и в настройках клуба для страницы.
        var broken = green.Clone();
        broken.Bg1 = "url(javascript:alert(1))";
        await rt.HandleCommandAsync(ConfigChanged(new ClubConfig { ClubName = "Asia", ShellTheme = broken }), CancellationToken.None);
        Assert.Null(snapshots[^1].ShellTheme);
        Assert.Null(snapshots[^1].Config.ShellTheme);

        // Стандартная тема (сервер не прислал shell_theme).
        await rt.HandleCommandAsync(ConfigChanged(new ClubConfig { ClubName = "Asia", ShellTheme = green }), CancellationToken.None);
        await rt.HandleCommandAsync(ConfigChanged(new ClubConfig { ClubName = "Asia" }), CancellationToken.None);
        Assert.Null(rt.State.ShellTheme);
    }

    [Fact]
    public async Task Тема_сохраняется_на_диске_и_экран_входа_в_ней_до_Welcome()
    {
        var file = Path.Combine(_dir.Path, "shell-theme.pb");
        await using (var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki))
        {
            Assert.Null(rt.State.BuildSnapshot().ShellTheme);
            rt.State.ApplyWelcome(new Welcome { Config = new ClubConfig { ShellTheme = Red() } });
            Assert.True(File.Exists(file));
        }

        // Перезагрузка ПК: до Welcome — сохранённая тема.
        await using (var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki))
        {
            Assert.Equal("#0a0304", rt.State.BuildSnapshot().ShellTheme.Bg0);
            // Клуб вернул стандартную тему — файл удаляется.
            rt.State.ApplyConfig(new ClubConfig());
            Assert.Null(rt.State.BuildSnapshot().ShellTheme);
            Assert.False(File.Exists(file));
        }

        await using (var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki))
        {
            Assert.Null(rt.State.ShellTheme);
        }
    }

    [Fact]
    public void Сохранённая_тема_не_перекрывает_настройки_клуба_и_проверяется()
    {
        var s = new AgentState();
        s.ApplyConfig(new ClubConfig());
        s.RestoreShellTheme(Red());
        Assert.Null(s.ShellTheme); // настройки уже пришли — в них стандартная тема

        var fresh = new AgentState();
        var broken = Red();
        broken.Accent = "red";
        fresh.RestoreShellTheme(broken);
        Assert.Null(fresh.ShellTheme);

        // Испорченный файл темы — стандартная тема.
        var file = Path.Combine(_dir.Path, "broken-theme.pb");
        File.WriteAllBytes(file, [0xFF, 0xFF, 0xFF]);
        Assert.Null(new ShellThemeStore(file).Saved);
    }

    [Fact]
    public void Ярлык_Zafiro_выводит_открытое_окно_вперёд()
    {
        var ui = new UiController();
        var activations = 0;
        ui.ActivateRequested += () => activations++;
        ui.OnConnection(true);

        // Экран входа открыт — ярлык выводит его вперёд (App.ShowShell(): окно входа — Activate).
        ui.OnSnapshot(new Snapshot { Screen = Screen.Lock, State = ComputerState.Idle });
        ui.RequestLauncher();
        Assert.Equal(UiView.Lock, ui.View);
        Assert.Equal(1, activations);

        // Идёт сессия, лаунчер скрыт — ярлык его открывает (Changed), вперёд выводит ApplyView.
        ui.OnSnapshot(new Snapshot { Screen = Screen.None, State = ComputerState.InSession, Guest = new GuestUser { Name = "А" } });
        ui.ReturnToDesktop();
        Assert.Equal(UiView.Hidden, ui.View);
        ui.RequestLauncher();
        Assert.Equal(UiView.Launcher, ui.View);
        Assert.Equal(1, activations);

        // Лаунчер открыт, но гость ушёл в игру — повторный ярлык выводит окно вперёд (раньше ничего не происходило).
        ui.RequestLauncher();
        Assert.Equal(2, activations);

        // Техрежим: окна нет — нечего выводить.
        ui.OnSnapshot(new Snapshot { Screen = Screen.None, State = ComputerState.Service });
        ui.RequestLauncher();
        Assert.Equal(UiView.Hidden, ui.View);
        Assert.Equal(2, activations);
    }
}
