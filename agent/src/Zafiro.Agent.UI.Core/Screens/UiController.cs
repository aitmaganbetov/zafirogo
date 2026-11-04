using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.UI.Screens;

/// <summary>Что показывает окно UI.</summary>
public enum UiView
{
    /// <summary>Окно скрыто, WebView2 освобождён (идёт сессия, техрежим).</summary>
    Hidden,

    /// <summary>Регистрация ПК (код из панели клуба). Без блокировки клавиш: сотрудник может свернуть окно.</summary>
    Setup,

    /// <summary>Экран входа / блокировки (без гостя — вход; гость без сессии — лаунчер поверх всех окон).</summary>
    Lock,

    /// <summary>Лаунчер во время сессии (гость открыл его сам: ярлык или после покупки).</summary>
    Launcher,
}

/// <summary>
/// Решение 5 (CLAUDE.md): окно с WebView2 существует, только пока нужен экран. По снимку службы (Snapshot.screen) и
/// просьбе гостя (ярлык «Zafiro», кнопка «Вернуться к игре») выбирает вид окна и режим киоска; процесс UI завершается,
/// когда смотреть нечего. Без WPF — проверяется unit-тестами.
/// </summary>
public sealed class UiController
{
    /// <summary>Сколько ждать первого подключения к службе, прежде чем выйти (службы нет — блокировать нечем).</summary>
    public static readonly TimeSpan FirstConnectTimeout = TimeSpan.FromSeconds(15);

    private readonly TimeProvider _time;
    private readonly DateTimeOffset _startedAt;
    private Screen _screen = Screen.Unspecified;
    private ComputerState _state = ComputerState.Unspecified;
    private bool _launcherRequested;
    private bool _everConnected;
    private bool _connected;

    public UiController(bool launcherRequested = false, TimeProvider? time = null)
    {
        _time = time ?? TimeProvider.System;
        _startedAt = _time.GetUtcNow();
        _launcherRequested = launcherRequested;
    }

    /// <summary>Текущий вид окна.</summary>
    public UiView View { get; private set; } = UiView.Hidden;

    /// <summary>Режим киоска: окно поверх всех, клавиши выхода и другие мониторы закрыты (нет оплаченной сессии).</summary>
    public bool Kiosk => View == UiView.Lock;

    /// <summary>Последний снимок службы.</summary>
    public Snapshot? Snapshot { get; private set; }

    /// <summary>Вид или режим киоска изменились.</summary>
    public event Action? Changed;

    /// <summary>Новый снимок службы.</summary>
    public void OnSnapshot(Snapshot snapshot)
    {
        ArgumentNullException.ThrowIfNull(snapshot);
        var previous = _screen;
        Snapshot = snapshot;
        _screen = snapshot.Screen;
        // Гость только что оплатил время на экране блокировки — лаунчер остаётся открытым (как старый агент после
        // покупки), пока гость сам не вернётся к игре. Техрежим и выключение — окно закрывается.
        if (previous == Screen.Lock && _screen == Screen.None && snapshot.State == ComputerState.InSession && snapshot.Guest is not null)
            _launcherRequested = true;
        if (snapshot.State is ComputerState.Service or ComputerState.ShuttingDown || snapshot.Guest is null && _screen == Screen.None)
            _launcherRequested = false;
        _state = snapshot.State;
        Recompute();
    }

    /// <summary>Канал к службе подключён / оборван.</summary>
    public void OnConnection(bool connected)
    {
        _connected = connected;
        if (connected) _everConnected = true;
        Recompute();
    }

    /// <summary>Гость открыл лаунчер (ярлык «Zafiro», второй запуск UI).</summary>
    public void RequestLauncher()
    {
        _launcherRequested = true;
        Recompute();
    }

    /// <summary>«Вернуться к игре»: лаунчер скрывается, если экран не нужен (иначе — ничего).</summary>
    public void ReturnToDesktop()
    {
        _launcherRequested = false;
        Recompute();
    }

    /// <summary>
    /// Процесс UI можно завершить: окно скрыто и нет уведомлений на экране; или служба так и не ответила. Обрыв канала
    /// после подключения — не повод: экран блокировки остаётся, пока служба не вернётся (обновление, перезапуск).
    /// </summary>
    public bool ShouldExit(bool noticesOpen)
    {
        if (!_everConnected) return _time.GetUtcNow() - _startedAt >= FirstConnectTimeout && !noticesOpen;
        // Снимка ещё нет — неизвестно, нужен ли экран: ждём его.
        return _connected && Snapshot is not null && View == UiView.Hidden && !noticesOpen;
    }

    private void Recompute()
    {
        var view = _screen switch
        {
            Screen.Setup => UiView.Setup,
            Screen.Lock => UiView.Lock,
            Screen.Launcher => UiView.Launcher,
            Screen.None => _launcherRequested && _state == ComputerState.InSession ? UiView.Launcher : UiView.Hidden,
            _ => View, // снимка ещё нет — как было
        };
        if (view == View) return;
        View = view;
        Changed?.Invoke();
    }
}
