using System.Diagnostics;
using Zafiro.Agent.UI.Screens;

namespace Zafiro.Agent.UI;

/// <summary>
/// Точка входа процесса UI. Служба запускает его в сеансе пользователя (<c>--screen …</c>), ярлык «Zafiro» —
/// с <c>--launcher</c>: если UI уже работает, ему отправляется сигнал открыть лаунчер и вывести окно вперёд (старый
/// агент, коммит 71db8d6: ярлык открывает запущенную оболочку, а не «агент уже запущен»), а второй процесс выходит.
/// Запуск службой проверку «уже работает» не делает: занятое чужим процессом имя (мьютекс, событие) не должно
/// мешать экрану блокировки появиться — второй UI служба и так не запускает (UiCoordinator ждёт, пока UI подключён).
/// </summary>
internal static class Program
{
    internal const string LauncherSignalName = @"Local\ZafiroAgent.UI.Launcher";

    [STAThread]
    private static int Main(string[] args)
    {
        var options = LaunchOptions.Parse(args);
        if (options.Launcher && EventWaitHandle.TryOpenExisting(LauncherSignalName, out var running))
        {
            using (running)
            {
                // Право на передний план — работающему UI: иначе окно лаунчера осталось бы за игрой.
                GrantForeground();
                running.Set();
            }

            return 0;
        }

        var app = new UiApp(options);
        return app.Run();
    }

    /// <summary>
    /// Отдаёт право на передний план именно работающему UI (его PID); если процесс не найден или Windows отказала —
    /// как раньше, любому процессу (ASFW_ANY): окно лаунчера важнее.
    /// </summary>
    private static void GrantForeground()
    {
        var pid = FindRunningUi();
        if (pid is null || !NativeMethods.AllowSetForegroundWindow(pid.Value))
        {
            _ = NativeMethods.AllowSetForegroundWindow(NativeMethods.AsfwAny);
        }
    }

    private static int? FindRunningUi()
    {
        try
        {
            using var self = Process.GetCurrentProcess();
            var found = new List<UiProcess>();
            foreach (var p in Process.GetProcessesByName(self.ProcessName))
            {
                using (p)
                {
                    try
                    {
                        found.Add(new UiProcess(p.Id, p.SessionId, p.StartTime));
                    }
                    catch (Exception ex) when (ex is InvalidOperationException or System.ComponentModel.Win32Exception or NotSupportedException)
                    {
                        // Процесс уже завершился или недоступен — не кандидат.
                    }
                }
            }

            return RunningUi.Pick(found, self.Id, self.SessionId);
        }
        catch (Exception ex) when (ex is InvalidOperationException or System.ComponentModel.Win32Exception or NotSupportedException)
        {
            return null;
        }
    }
}
