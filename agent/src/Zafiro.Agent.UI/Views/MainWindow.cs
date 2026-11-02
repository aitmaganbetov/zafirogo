using System.IO;
using System.Runtime.InteropServices;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Interop;
using System.Windows.Media;
using System.Windows.Threading;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.Wpf;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.UI.Bridge;

namespace Zafiro.Agent.UI.Views;

/// <summary>
/// Окно UI: WebView2 на весь экран со страницей Frontend\index.html. WebView2 создаётся вместе с окном и
/// освобождается при его закрытии (окно закрывается, как только экран не нужен). Безопасность страницы:
/// только виртуальный хост <see cref="AppHost"/> (папка Frontend рядом с exe), запросы к другим адресам отклоняются,
/// CSP — в index.html, новые окна, загрузки, разрешения (камера, микрофон…) — запрещены, автозаполнение и сохранение
/// паролей выключены; в Release — без DevTools, контекстного меню и горячих клавиш браузера. Сообщения принимаются
/// только от страницы <see cref="AppOrigin"/> и только через <see cref="BridgeRouter"/>.
/// Перенесено из старого агента: MainWindow.xaml.cs InitializeWebViewAsync(), GuardLockedScreen(), RecreateWebViewAsync().
/// </summary>
internal sealed class MainWindow : Window
{
    /// <summary>Виртуальный хост страницы (.invalid — имя, которое не разрешается в сети).</summary>
    public const string AppHost = "app.zafiro.invalid";
    public const string AppOrigin = "https://" + AppHost + "/";

    private readonly BridgeRouter _bridge;
    private readonly Func<Snapshot?> _snapshot;
    private readonly Grid _root = new();
    private readonly TextBlock _error;
    private readonly DispatcherTimer _guard = new() { Interval = TimeSpan.FromSeconds(1) };
    private readonly CancellationTokenSource _closing = new();
    private readonly Queue<string> _beforeLoad = new();
    private WebView2? _web;
    private bool _loaded;
    private bool _kiosk;
    private bool _releasing;
    private DateTime _recreatedAtUtc;

    public MainWindow(BridgeRouter bridge, Func<Snapshot?> snapshot)
    {
        _bridge = bridge;
        _snapshot = snapshot;
        Title = "Zafiro";
        WindowStyle = WindowStyle.None;
        ResizeMode = ResizeMode.NoResize;
        WindowState = WindowState.Maximized;
        Background = new SolidColorBrush(Color.FromRgb(0x0B, 0x0D, 0x12));
        UseLayoutRounding = true;
        ShowInTaskbar = false;
        _error = new TextBlock
        {
            Visibility = Visibility.Collapsed,
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Center,
            TextAlignment = TextAlignment.Center,
            TextWrapping = TextWrapping.Wrap,
            MaxWidth = 640,
            FontSize = 22,
            FontFamily = new FontFamily("Segoe UI"),
            Foreground = new SolidColorBrush(Color.FromRgb(0xF3, 0xF4, 0xF6)),
            Text = "Интерфейс агента не загрузился. Обратитесь к администратору клуба.",
        };
        _root.Children.Add(_error);
        Content = _root;
        Loaded += async (_, _) => await StartWebViewAsync().ConfigureAwait(true);
        Closing += (_, args) =>
        {
            // Режим киоска: закрыть окно (Alt+F4) нельзя — только служба снимает блокировку.
            if (_kiosk && !_releasing) args.Cancel = true;
        };
        Closed += (_, _) => ReleaseWebView();
        _guard.Tick += (_, _) => GuardLockedScreen();
    }

    /// <summary>Режим киоска: поверх всех окон, чужие окна на переднем плане сворачиваются.</summary>
    public void SetKiosk(bool kiosk)
    {
        _kiosk = kiosk;
        Topmost = kiosk;
        // Лаунчер во время сессии — с кнопкой на панели задач (гость переключается между ним и игрой).
        ShowInTaskbar = !kiosk;
        if (kiosk)
        {
            // Сессия кончилась: игры и приложения сворачиваются, окно UI — снова развёрнуто поверх всех.
            NativeMethods.MinimizeAllWindows();
            Dispatcher.BeginInvoke(RestoreOnTop, DispatcherPriority.Background);
            _guard.Start();
        }
        else
        {
            _guard.Stop();
        }
    }

    /// <summary>Закрыть окно и освободить WebView2 (экран больше не нужен).</summary>
    public void CloseAndRelease()
    {
        _releasing = true;
        _guard.Stop();
        Close();
    }

    /// <summary>Сообщение странице (JSON); до загрузки страницы — в очередь.</summary>
    public void PostJson(string json)
    {
        if (!_loaded || _web?.CoreWebView2 is null)
        {
            if (_beforeLoad.Count < 64) _beforeLoad.Enqueue(json);
            return;
        }

        try
        {
            _web.CoreWebView2.PostWebMessageAsJson(json);
        }
        catch (Exception ex) when (ex is InvalidOperationException or COMException)
        {
            UiLog.Write("post to page failed: " + ex.Message);
        }
    }

    /// <summary>Событие процесса UI (не службы): staffPin, serviceConnected, serviceDisconnected.</summary>
    public void PostLocalEvent(string name) => PostJson("{\"local\":\"" + name + "\"}");

    private async Task StartWebViewAsync()
    {
        try
        {
            _web = new WebView2();
            _root.Children.Insert(0, _web);
            var environment = await CoreWebView2Environment.CreateAsync(null, WebViewDataFolder()).ConfigureAwait(true);
            await _web.EnsureCoreWebView2Async(environment).ConfigureAwait(true);
            Configure(_web.CoreWebView2, environment);
        }
        catch (Exception ex) when (ex is COMException or WebView2RuntimeNotFoundException or InvalidOperationException or IOException or UnauthorizedAccessException)
        {
            // Без страницы ПК остаётся заблокированным (окно поверх всех), сотрудник выходит по PIN-коду с панели.
            UiLog.Write("webview failed to start: " + ex.Message);
            ShowError();
        }
    }

    private void Configure(CoreWebView2 core, CoreWebView2Environment environment)
    {
        var settings = core.Settings;
        settings.AreDefaultContextMenusEnabled = false;
        settings.IsStatusBarEnabled = false;
        settings.IsZoomControlEnabled = false;
        settings.IsPinchZoomEnabled = false;
        settings.IsSwipeNavigationEnabled = false;
        settings.IsGeneralAutofillEnabled = false;
        settings.IsPasswordAutosaveEnabled = false;
        settings.AreHostObjectsAllowed = false;
        settings.AreDefaultScriptDialogsEnabled = false;
        settings.IsBuiltInErrorPageEnabled = false;
        settings.IsWebMessageEnabled = true;
#if DEBUG
        settings.AreDevToolsEnabled = true;
#else
        // Без консоли и горячих клавиш браузера гость не может слать мосту свои сообщения.
        settings.AreDevToolsEnabled = false;
        settings.AreBrowserAcceleratorKeysEnabled = false;
#endif
        core.NavigationStarting += (_, args) =>
        {
            if (!IsAppUrl(args.Uri)) args.Cancel = true;
        };
        core.FrameNavigationStarting += (_, args) => args.Cancel = true;
        core.NewWindowRequested += (_, args) => args.Handled = true;
        core.DownloadStarting += (_, args) => args.Cancel = true;
        core.PermissionRequested += (_, args) => args.State = CoreWebView2PermissionState.Deny;
        // Любой запрос не к виртуальному хосту (картинки, fetch, шрифты) — 403, даже если CSP пропустит.
        core.AddWebResourceRequestedFilter("*", CoreWebView2WebResourceContext.All);
        core.WebResourceRequested += (_, args) =>
        {
            if (!IsAppUrl(args.Request.Uri) && !args.Request.Uri.StartsWith("data:", StringComparison.Ordinal))
                args.Response = environment.CreateWebResourceResponse(null, 403, "Forbidden", "");
        };
        core.ProcessFailed += (_, args) =>
        {
            // Гость может завершить процессы msedgewebview2.exe: без этого остался бы пустой экран.
            UiLog.Write("webview process failed: " + args.ProcessFailedKind);
            if (args.ProcessFailedKind == CoreWebView2ProcessFailedKind.BrowserProcessExited)
                Dispatcher.BeginInvoke(() => _ = RecreateWebViewAsync());
            else
                Dispatcher.BeginInvoke(() =>
                {
                    try
                    {
                        _web?.CoreWebView2?.Reload();
                    }
                    catch (Exception ex) when (ex is InvalidOperationException or COMException)
                    {
                        UiLog.Write("webview reload failed: " + ex.Message);
                    }
                });
        };
        core.WebMessageReceived += OnWebMessage;
        core.NavigationCompleted += (_, args) =>
        {
            _loaded = args.IsSuccess;
            if (!args.IsSuccess) return;
            while (_beforeLoad.Count > 0) PostJson(_beforeLoad.Dequeue());
        };

        var frontend = Path.Combine(AppContext.BaseDirectory, "Frontend");
        if (!File.Exists(Path.Combine(frontend, "index.html")))
        {
            UiLog.Write("frontend not found: " + frontend);
            ShowError();
            return;
        }

        core.SetVirtualHostNameToFolderMapping(AppHost, frontend, CoreWebView2HostResourceAccessKind.DenyCors);
        core.Navigate(AppOrigin + "index.html");
    }

    private async void OnWebMessage(object? sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        // Команды — только от собственной страницы (не от фрейма, не от другого адреса).
        if (!IsAppUrl(e.Source)) return;
        string json;
        try
        {
            json = e.WebMessageAsJson;
        }
        catch (ArgumentException)
        {
            return;
        }

        try
        {
            var reply = await _bridge.HandleAsync(json, _closing.Token).ConfigureAwait(true);
            if (reply is not null) PostJson(reply);
        }
        catch (OperationCanceledException)
        {
        }
    }

    private static bool IsAppUrl(string? url) => url is not null && url.StartsWith(AppOrigin, StringComparison.OrdinalIgnoreCase);

    private void ShowError()
    {
        if (_web is not null) _web.Visibility = Visibility.Collapsed;
        _error.Visibility = Visibility.Visible;
    }

    /// <summary>Браузер WebView2 завершился: элемент создаётся заново (не чаще раза в 10 с).</summary>
    private async Task RecreateWebViewAsync()
    {
        if (_releasing || DateTime.UtcNow - _recreatedAtUtc < TimeSpan.FromSeconds(10)) return;
        _recreatedAtUtc = DateTime.UtcNow;
        _loaded = false;
        ReleaseWebView();
        await StartWebViewAsync().ConfigureAwait(true);
        if (_snapshot() is { } snap) PostJson(BridgeRouter.EventJson(new Event { Snapshot = snap }));
    }

    private void ReleaseWebView()
    {
        _loaded = false;
        var web = _web;
        _web = null;
        if (web is null) return;
        _root.Children.Remove(web);
        try
        {
            web.Dispose();
        }
        catch (Exception ex) when (ex is InvalidOperationException or COMException)
        {
            UiLog.Write("webview dispose failed: " + ex.Message);
        }
    }

    protected override void OnClosed(EventArgs e)
    {
        _closing.Cancel();
        _closing.Dispose();
        base.OnClosed(e);
    }

    /// <summary>
    /// Без оплаченной сессии на переднем плане может быть только UI: окно другой программы (игра на весь экран после
    /// конца сессии) сворачивается; панель задач и рабочий стол не трогаются — окно просто встаёт поверх них.
    /// </summary>
    private void RestoreOnTop()
    {
        if (WindowState == WindowState.Minimized) WindowState = WindowState.Maximized;
        Activate();
    }

    private void GuardLockedScreen()
    {
        if (!_kiosk || !IsVisible) return;
        if (WindowState == WindowState.Minimized)
        {
            RestoreOnTop();
            return;
        }

        var foreground = NativeMethods.GetForegroundWindow();
        if (foreground == IntPtr.Zero) return;
        _ = NativeMethods.GetWindowThreadProcessId(foreground, out var processId);
        if (processId == (uint)Environment.ProcessId) return;
        var cls = NativeMethods.ClassName(foreground);
        if (cls is not ("Shell_TrayWnd" or "Shell_SecondaryTrayWnd" or "Progman" or "WorkerW"))
            NativeMethods.ShowWindow(foreground, NativeMethods.SwMinimize);
        RestoreOnTop();
    }

    /// <summary>
    /// Данные WebView2 — в профиле пользователя (рядом с exe писать нельзя). Подменённая гостем папка (файл, ссылка)
    /// пересоздаётся, не вышло — временная.
    /// </summary>
    private static string WebViewDataFolder()
    {
        var folder = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "ZafiroAgent", "WebView2");
        try
        {
            if (File.Exists(folder)) File.Delete(folder);
            var dir = new DirectoryInfo(folder);
            if (dir.Exists && (dir.Attributes & FileAttributes.ReparsePoint) != 0) dir.Delete();
            Directory.CreateDirectory(folder);
            return folder;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            UiLog.Write("webview data folder is unusable: " + ex.Message);
            return Path.Combine(Path.GetTempPath(), "ZafiroAgent-WebView2-" + Guid.NewGuid().ToString("N"));
        }
    }

    internal IntPtr Handle => new WindowInteropHelper(this).Handle;
}
