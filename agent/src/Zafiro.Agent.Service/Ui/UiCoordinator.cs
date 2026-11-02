using System.Runtime.Versioning;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Service.Platform;

namespace Zafiro.Agent.Service.Ui;

/// <summary>Запуск процесса UI в сеансе пользователя.</summary>
public interface IUiLauncher
{
    /// <summary>Запустить UI (аргумент — какой экран нужен). false — запустить нельзя (никто не вошёл, нет exe).</summary>
    bool Launch(Screen screen);
}

/// <summary>
/// Решение 5 (CLAUDE.md): UI (WPF + WebView2) живёт только пока нужен экран — блокировка, вход, регистрация ПК,
/// сообщение сотрудника. Служба смотрит на <see cref="AgentState.Screen"/>: экран нужен, а UI не подключён к IPC —
/// запускает UI в сеансе консоли (повторы с паузой 3 с … 1 мин, как Watchdog.RetryDelay старого агента). Экран не
/// нужен (идёт сессия) — UI получает снимок со screen = NONE и закрывается сам, освобождая WebView2.
/// </summary>
public sealed class UiCoordinator : BackgroundService
{
    private static readonly TimeSpan Check = TimeSpan.FromSeconds(3);

    private readonly AgentRuntime _runtime;
    private readonly IpcServer _ipc;
    private readonly IUiLauncher _launcher;
    private readonly TimeProvider _time;
    private readonly ILogger _log;
    // Предупреждения о конце сессии: за сколько минут (WarnSessionTime старого агента, MainWindow.xaml.cs).
    private static readonly int[] SessionWarningMinutes = [10, 5, 3, 1];

    private int _failures;
    private DateTimeOffset _nextAttempt;
    private DateTimeOffset? _warnedForEnd;
    private int _warnedMinutes = int.MaxValue;

    public UiCoordinator(AgentRuntime runtime, IpcServer ipc, IUiLauncher launcher, TimeProvider time, ILogger<UiCoordinator> log)
    {
        _runtime = runtime;
        _ipc = ipc;
        _launcher = launcher;
        _time = time;
        _log = log;
    }

    /// <summary>Пауза перед следующим запуском, если UI сразу закрывается: 3 с, 6 с, 12 с … до 1 мин.</summary>
    public static TimeSpan RetryDelay(int failures) => TimeSpan.FromSeconds(Math.Min(60, 3 * Math.Pow(2, Math.Clamp(failures - 1, 0, 5))));

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        try
        {
            using var timer = new PeriodicTimer(Check, _time);
            do
            {
                Tick();
            }
            while (await timer.WaitForNextTickAsync(stoppingToken).ConfigureAwait(false));
        }
        catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
        {
        }
    }

    /// <summary>
    /// За 10, 5, 3 и 1 минуту до конца сессии — уведомление, каждое один раз. Время продлили — отсчёт заново, но без
    /// уведомления сразу после покупки. Перенесено из старого агента: MainWindow.xaml.cs WarnSessionTime().
    /// </summary>
    internal SessionEnding? CheckSessionEnding(DateTimeOffset? endsAt, DateTimeOffset serverNow)
    {
        if (endsAt is not { } end)
        {
            _warnedForEnd = null;
            return null;
        }

        var left = end - serverNow;
        var reached = int.MaxValue;
        foreach (var minutes in SessionWarningMinutes)
            if (left <= TimeSpan.FromMinutes(minutes)) reached = Math.Min(reached, minutes);

        if (_warnedForEnd != end)
        {
            _warnedForEnd = end;
            _warnedMinutes = reached;
            return null;
        }

        if (reached >= _warnedMinutes || left <= TimeSpan.Zero) return null;
        _warnedMinutes = reached;
        return new SessionEnding
        {
            MinutesLeft = Math.Max(1, (int)Math.Ceiling(left.TotalMinutes)),
            EndsAt = Google.Protobuf.WellKnownTypes.Timestamp.FromDateTimeOffset(end),
        };
    }

    private void Tick()
    {
        var snap = _runtime.State.BuildSnapshot();
        if (CheckSessionEnding(snap.Session?.EndsAt?.ToDateTimeOffset(), snap.ServerTime.ToDateTimeOffset()) is { } ending)
            _ipc.Publish(new Event { SessionEnding = ending });

        if (_ipc.ConnectedClients > 0)
        {
            _failures = 0;
            return;
        }

        var screen = snap.Screen;
        if (screen == Screen.None && !_ipc.HasPendingNotices) return;
        if (_time.GetUtcNow() < _nextAttempt) return;
        var launched = _launcher.Launch(screen == Screen.None ? Screen.Lock : screen);
        _failures++;
        _nextAttempt = _time.GetUtcNow() + RetryDelay(_failures);
        if (launched) _log.LogInformation("ui started for screen {Screen}", screen);
    }
}

/// <summary>Linux: UI не запускается (тесты подключаются к unix socket сами).</summary>
public sealed class NoUiLauncher : IUiLauncher
{
    public bool Launch(Screen screen) => false;
}

/// <summary>Windows: UI в активном сеансе консоли (CreateProcessAsUser), не под администратором, если это не разрешено установщиком.</summary>
[SupportedOSPlatform("windows")]
public sealed class WindowsUiLauncher : IUiLauncher
{
    private readonly string _exe;
    private readonly bool _forAdministrators;
    private readonly ILogger _log;

    public WindowsUiLauncher(ServiceOptions options, ILogger<WindowsUiLauncher> log)
    {
        ArgumentNullException.ThrowIfNull(options);
        _exe = options.UiExecutable;
        _forAdministrators = options.LaunchUiForAdministrators;
        _log = log;
    }

    public bool Launch(Screen screen)
    {
        if (string.IsNullOrEmpty(_exe) || !File.Exists(_exe)) return false;
        var session = WindowsNative.WTSGetActiveConsoleSessionId();
        if (session == WindowsNative.NoSession || session == 0) return false;
        if (!WindowsNative.WTSQueryUserToken(session, out var token)) return false; // никто не вошёл
        try
        {
            if (!_forAdministrators && WindowsNative.IsAdministrator(token)) return false;
            var arg = screen switch
            {
                Screen.Setup => "--screen setup",
                Screen.Launcher => "--screen launcher",
                _ => "--screen lock",
            };
            WindowsNative.LaunchAsUser(token, _exe, arg);
            return true;
        }
        catch (System.ComponentModel.Win32Exception ex)
        {
            _log.LogWarning("could not start the agent UI: {Error}", ex.Message);
            return false;
        }
        finally
        {
            WindowsNative.CloseHandle(token);
        }
    }
}
