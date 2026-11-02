namespace Zafiro.Agent.UI.Screens;

/// <summary>
/// Аргументы процесса UI. Служба запускает UI с <c>--screen setup|lock|launcher</c> (WindowsUiLauncher; экран всё равно
/// берётся из снимка службы), ярлык «Zafiro» — с <c>--launcher</c> (открыть лаунчер во время сессии). Неизвестные
/// аргументы игнорируются.
/// </summary>
public sealed record LaunchOptions(bool Launcher, string PipeName)
{
    public const string DefaultPipeName = "ZafiroAgent.Ipc";

    public static LaunchOptions Parse(IEnumerable<string> args)
    {
        ArgumentNullException.ThrowIfNull(args);
        var launcher = false;
        var pipe = DefaultPipeName;
        var previous = "";
        foreach (var arg in args)
        {
            if (arg == "--launcher" || (previous == "--screen" && arg == "launcher")) launcher = true;
            // Имя канала — только для отладки на стенде: служба всё равно проверяет exe и SID клиента.
            else if (arg.StartsWith("--pipe=", StringComparison.Ordinal) && arg.Length is > 7 and <= 64 &&
                     arg[7..].All(c => char.IsAsciiLetterOrDigit(c) || c is '.' or '-' or '_'))
                pipe = arg[7..];
            previous = arg;
        }

        return new LaunchOptions(launcher, pipe);
    }
}
