using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Windows.Input;
using Zafiro.Agent.UI.Bridge;

namespace Zafiro.Agent.UI.Platform;

/// <summary>Локальные действия страницы (BridgeRouter, методы ui.*) — в процессе UI, от имени пользователя сеанса.</summary>
internal sealed class UiHost : IUiHost
{
    private readonly UiApp _app;

    public UiHost(UiApp app) => _app = app;

    public void ReturnToDesktop() => _app.ReturnToDesktop();

    /// <summary>
    /// Кнопки выключения и перезагрузки на экране входа: shutdown.exe с /f (открытые игры не мешают).
    /// Перенесено из старого агента: Infrastructure/PowerControl.cs Run().
    /// </summary>
    public bool Power(bool restart)
    {
        try
        {
            using var process = Process.Start(new ProcessStartInfo
            {
                FileName = Path.Combine(Environment.SystemDirectory, "shutdown.exe"),
                Arguments = (restart ? "/r" : "/s") + " /f /t 0",
                UseShellExecute = false,
                CreateNoWindow = true,
            });
            if (process is null) return false;
            if (!process.WaitForExit(10000)) return true; // Windows уже выключается
            if (process.ExitCode == 0) return true;
            UiLog.Write("shutdown.exe exited with code " + process.ExitCode.ToString(CultureInfo.InvariantCulture));
        }
        catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or InvalidOperationException or IOException)
        {
            UiLog.Write("shutdown.exe failed: " + ex.Message);
        }

        return false;
    }

    /// <summary>
    /// Язык ввода Windows у поля пароля (панели задач в киоске нет): РУС, ҚАЗ, ENG; next — следующий, как Alt+Shift.
    /// Перенесено из старого агента: Views/ClubSetupWindow.xaml.cs ShowInputLanguage(), NextInputLanguage().
    /// </summary>
    public string InputLanguage(bool next)
    {
        var manager = InputLanguageManager.Current;
        if (next)
        {
            try
            {
                var languages = manager.AvailableInputLanguages?.Cast<CultureInfo>().ToList();
                if (languages is { Count: > 1 })
                {
                    var index = languages.FindIndex(language => language.Equals(manager.CurrentInputLanguage));
                    manager.CurrentInputLanguage = languages[(index + 1) % languages.Count];
                }
            }
            catch (Exception ex) when (ex is ArgumentException or InvalidOperationException or CultureNotFoundException)
            {
                UiLog.Write("input language switch failed: " + ex.Message);
            }
        }

        var culture = manager.CurrentInputLanguage ?? CultureInfo.CurrentCulture;
        return culture.TwoLetterISOLanguageName switch
        {
            "ru" => "РУС",
            "kk" => "ҚАЗ",
            "en" => "ENG",
            _ => culture.ThreeLetterWindowsLanguageName.ToUpperInvariant(),
        };
    }
}
