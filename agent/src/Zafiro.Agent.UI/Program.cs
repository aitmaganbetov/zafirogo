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
                _ = NativeMethods.AllowSetForegroundWindow(NativeMethods.AsfwAny);
                running.Set();
            }

            return 0;
        }

        var app = new UiApp(options);
        return app.Run();
    }
}
