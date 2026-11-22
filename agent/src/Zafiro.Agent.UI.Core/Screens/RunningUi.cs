namespace Zafiro.Agent.UI.Screens;

/// <summary>Процесс с именем UI агента: кандидат в «уже работающий экземпляр».</summary>
public readonly record struct UiProcess(int Id, int SessionId, DateTime StartTime);

/// <summary>
/// Выбор работающего экземпляра UI для второго запуска (ярлык «Zafiro»): ему, а не любому процессу, отдаётся право
/// вывести окно на передний план (AllowSetForegroundWindow с PID вместо ASFW_ANY — иначе правом мог воспользоваться
/// любой процесс сеанса, например окно поверх экрана блокировки).
/// </summary>
public static class RunningUi
{
    /// <summary>
    /// PID работающего UI: процесс с тем же именем в том же сеансе Windows, кроме самого себя; из нескольких — самый
    /// ранний (работающий экземпляр запущен раньше ярлыка). null — не найден (вызывающий оставляет прежнее поведение).
    /// </summary>
    public static int? Pick(IEnumerable<UiProcess> sameName, int selfId, int selfSessionId)
    {
        ArgumentNullException.ThrowIfNull(sameName);
        UiProcess? best = null;
        foreach (var p in sameName)
        {
            if (p.Id <= 0 || p.Id == selfId || p.SessionId != selfSessionId) continue;
            if (best is null || p.StartTime < best.Value.StartTime) best = p;
        }

        return best?.Id;
    }
}
