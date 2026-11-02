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
    private int _failures;
    private DateTimeOffset _nextAttempt;
    private volatile bool _noticePending;

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
        _runtime.UiEvent += OnUiEvent;
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
        finally
        {
            _runtime.UiEvent -= OnUiEvent;
        }
    }

    private void OnUiEvent(Event e)
    {
        if (e.KindCase == Event.KindOneofCase.ShowMessage) _noticePending = true;
    }

    private void Tick()
    {
        if (_ipc.ConnectedClients > 0)
        {
            _failures = 0;
            _noticePending = false;
            return;
        }

        var screen = _runtime.State.Screen;
        if (screen == Screen.None && !_noticePending) return;
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
