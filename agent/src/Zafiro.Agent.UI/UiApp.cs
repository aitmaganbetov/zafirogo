using System.IO;
using System.Threading;
using System.Windows;
using System.Windows.Threading;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.UI.Bridge;
using Zafiro.Agent.UI.Ipc;
using Zafiro.Agent.UI.Kiosk;
using Zafiro.Agent.UI.Platform;
using Zafiro.Agent.UI.Screens;
using Zafiro.Agent.UI.Views;

namespace Zafiro.Agent.UI;

/// <summary>
/// Приложение UI: канал к службе (<see cref="IpcClient"/>), выбор экрана (<see cref="UiController"/>), окно с WebView2
/// (<see cref="MainWindow"/> — создаётся при показе и закрывается со скрытием), уведомления в углу экрана
/// (<see cref="CornerNotice"/> — без WebView2), киоск (перехват клавиш, другие мониторы закрыты). Окна нет и
/// уведомлений нет — процесс завершается: во время сессии UI не тратит память и CPU.
/// </summary>
internal sealed class UiApp : Application
{
    private readonly LaunchOptions _options;
    private readonly CancellationTokenSource _stop = new();
    private readonly UiController _ui;
    private readonly IpcClient _ipc;
    private readonly BridgeRouter _bridge;
    private readonly UiTranslations _texts;
    private readonly KioskKeyboard _keyboard = new();
    private readonly ScreenCover _covers = new();
    private readonly DispatcherTimer _exitCheck = new() { Interval = TimeSpan.FromSeconds(2) };
    private EventWaitHandle? _launcherSignal;
    private RegisteredWaitHandle? _launcherWait;
    private MainWindow? _window;

    public UiApp(LaunchOptions options)
    {
        _options = options;
        ShutdownMode = ShutdownMode.OnExplicitShutdown;
        _ui = new UiController(options.Launcher);
        _ipc = new IpcClient(IpcClient.NamedPipe(options.PipeName));
        _bridge = new BridgeRouter(_ipc, new UiHost(this));
        _texts = new UiTranslations(_ipc);
    }

    /// <summary>Тексты для окон без WebView2 (уведомления): переводы службы, ключ — русский текст.</summary>
    internal UiTranslations Texts => _texts;

    protected override void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);
        DispatcherUnhandledException += (_, args) =>
        {
            // Сбой страницы или окна не должен снимать блокировку: окно пересоздаётся при следующем снимке.
            UiLog.Write("unhandled: " + args.Exception.GetType().Name + ": " + args.Exception.Message);
            args.Handled = true;
        };

        try
        {
            _launcherSignal = new EventWaitHandle(false, EventResetMode.AutoReset, Program.LauncherSignalName, out var created);
            if (created)
            {
                _launcherWait = ThreadPool.RegisterWaitForSingleObject(_launcherSignal,
                    (_, _) => Dispatcher.BeginInvoke(_ui.RequestLauncher), null, Timeout.Infinite, executeOnlyOnce: false);
            }
        }
        catch (Exception ex) when (ex is WaitHandleCannotBeOpenedException or UnauthorizedAccessException or IOException)
        {
            UiLog.Write("launcher signal unavailable: " + ex.Message);
        }

        _ui.Changed += ApplyView;
        _ipc.EventReceived += ev => Dispatcher.BeginInvoke(() => OnServiceEvent(ev));
        _ipc.ConnectionChanged += connected => Dispatcher.BeginInvoke(() => OnConnection(connected));
        _keyboard.StaffShortcut += () => Dispatcher.BeginInvoke(() => _window?.PostLocalEvent("staffPin"));
        _keyboard.Install();
        _exitCheck.Tick += (_, _) =>
        {
            if (_ui.ShouldExit(CornerNotice.AnyOpen)) Shutdown();
        };
        _exitCheck.Start();
        _ = _ipc.RunAsync(_stop.Token);
    }

    protected override void OnExit(ExitEventArgs e)
    {
        _exitCheck.Stop();
        _stop.Cancel();
        _launcherWait?.Unregister(null);
        _launcherSignal?.Dispose();
        _keyboard.Dispose();
        _covers.Dispose();
        CloseWindow();
        _ipc.DisposeAsync().AsTask().GetAwaiter().GetResult();
        _stop.Dispose();
        base.OnExit(e);
    }

    /// <summary>«Вернуться к игре» со страницы.</summary>
    internal void ReturnToDesktop() => _ui.ReturnToDesktop();

    private void OnConnection(bool connected)
    {
        _ui.OnConnection(connected);
        _window?.PostLocalEvent(connected ? "serviceConnected" : "serviceDisconnected");
    }

    private void OnServiceEvent(Event ev)
    {
        switch (ev.KindCase)
        {
            case Event.KindOneofCase.Snapshot:
                _ui.OnSnapshot(ev.Snapshot);
                _covers.SetClubName(ev.Snapshot.Config?.ClubName ?? ev.Snapshot.ClubName);
                _ = _texts.RefreshAsync(ev.Snapshot.Config?.DefaultLocale, ev.Snapshot.TranslationsVersion, _stop.Token);
                break;
            case Event.KindOneofCase.TranslationsUpdated:
                _ = _texts.RefreshAsync(null, ev.TranslationsUpdated.Version, _stop.Token);
                break;
            case Event.KindOneofCase.ShowMessage:
                CornerNotice.Show(CornerNotice.Kind.StaffMessage, _texts.T("Сообщение администратора"), ev.ShowMessage.Text,
                    ev.ShowMessage.DisplayFor?.ToTimeSpan());
                break;
            case Event.KindOneofCase.SessionEnding:
                CornerNotice.Show(CornerNotice.Kind.SessionTime, null,
                    _texts.T("До конца сессии осталось :count мин.", ("count", ev.SessionEnding.MinutesLeft.ToString(System.Globalization.CultureInfo.InvariantCulture))));
                break;
            case Event.KindOneofCase.ChatMessage when ev.ChatMessage.Message is { FromStaff: true } message && _window is not { IsActive: true }:
                var body = message.Body.Length > 300 ? message.Body[..300] + "…" : message.Body;
                if (!string.IsNullOrWhiteSpace(body)) CornerNotice.Show(CornerNotice.Kind.Chat, _texts.T("Новое сообщение в чате"), body);
                break;
        }

        _window?.PostJson(BridgeRouter.EventJson(ev));
    }

    private void ApplyView()
    {
        var view = _ui.View;
        if (view == UiView.Hidden)
        {
            CloseWindow();
            _covers.Update(null, false);
            _keyboard.Enabled = false;
            return;
        }

        if (_window is null)
        {
            _window = new MainWindow(_bridge, () => _ui.Snapshot);
            _window.Closed += (_, _) =>
            {
                _window = null;
                // Окно закрыли не мы (Alt+F4 вне киоска, сбой): нужен экран — откроется снова.
                if (_ui.View != UiView.Hidden) Dispatcher.BeginInvoke(ApplyView, DispatcherPriority.Background);
            };
            _window.Show();
        }

        var kiosk = _ui.Kiosk;
        _window.SetKiosk(kiosk);
        _covers.Update(_window, kiosk);
        _keyboard.Enabled = kiosk;
        _keyboard.Strict = kiosk;
        if (view == UiView.Launcher) _window.Activate();
    }

    private void CloseWindow()
    {
        var window = _window;
        _window = null;
        window?.CloseAndRelease();
    }
}
