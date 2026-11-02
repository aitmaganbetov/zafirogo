using System.Runtime.InteropServices;
using System.Text;

namespace Zafiro.Agent.UI;

/// <summary>Win32 для окна UI (перенесено из старого агента: MainWindow.xaml.cs).</summary>
internal static class NativeMethods
{
    public const int SwMinimize = 6;

    [DllImport("shell32.dll")]
    private static extern void SHMinimizeAll();

    [DllImport("user32.dll")]
    public static extern IntPtr GetForegroundWindow();

    [DllImport("user32.dll")]
    public static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool ShowWindow(IntPtr window, int command);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern int GetClassName(IntPtr window, StringBuilder className, int maxCount);

    public static string ClassName(IntPtr window)
    {
        var name = new StringBuilder(64);
        _ = GetClassName(window, name, name.Capacity);
        return name.ToString();
    }

    /// <summary>Свернуть все окна (сессия кончилась — игры и приложения уходят, UI встаёт поверх).</summary>
    public static void MinimizeAllWindows()
    {
        try
        {
            SHMinimizeAll();
        }
        catch (Exception ex) when (ex is COMException or EntryPointNotFoundException or DllNotFoundException)
        {
            UiLog.Write("could not minimize windows: " + ex.Message);
        }
    }
}
