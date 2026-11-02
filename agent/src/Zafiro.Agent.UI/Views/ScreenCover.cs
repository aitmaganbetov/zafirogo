using System.Windows;
using System.Windows.Controls;
using System.Windows.Interop;
using System.Windows.Media;
using Microsoft.Win32;

namespace Zafiro.Agent.UI.Views;

/// <summary>
/// Без оплаченной сессии UI закрывает все мониторы: на мониторе окна UI — само окно, на остальных — чёрные окна поверх
/// всех с названием клуба. Подключённый позже монитор закрывается сразу. Логотип клуба (как в старом агенте) не
/// показывается: у UI нет сети (open-questions/stage5-agent.md AG-13).
/// Перенесено из старого агента: Kiosk/ScreenCover.cs.
/// </summary>
internal sealed class ScreenCover : IDisposable
{
    private readonly List<Window> _covers = [];
    private Window? _keep;
    private bool _shown;
    private string _clubName = "";
    private ImageSource? _logo;

    public ScreenCover()
    {
        SystemEvents.DisplaySettingsChanged += OnDisplaysChanged;
    }

    /// <summary>show = true: закрыть все мониторы, кроме монитора окна keep; false — убрать.</summary>
    public void Update(Window? keep, bool show)
    {
        _keep = keep;
        _shown = show;
        Rebuild();
    }

    public void SetClubName(string? name)
    {
        var value = name?.Trim() ?? "";
        if (value.Length == 0 || value == _clubName) return;
        _clubName = value;
        Refresh();
    }

    /// <summary>Логотип клуба над названием (как Kiosk/ScreenCover.cs старого агента; байты — из службы, AG-13). null — без логотипа.</summary>
    public void SetLogo(ImageSource? logo)
    {
        if (ReferenceEquals(logo, _logo)) return;
        _logo = logo;
        Refresh();
    }

    private void Refresh()
    {
        foreach (var cover in _covers) cover.Content = Branding();
    }

    // Логотип (не больше 480×320) и название клуба по центру — Branding() старого агента.
    private UIElement Branding()
    {
        var panel = new StackPanel
        {
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Center,
            Margin = new Thickness(48),
            IsHitTestVisible = false,
        };
        if (_logo is not null)
            panel.Children.Add(new Image { Source = _logo, Stretch = Stretch.Uniform, MaxWidth = 480, MaxHeight = 320, Margin = new Thickness(0, 0, 0, 24) });
        panel.Children.Add(new TextBlock
        {
            Text = _clubName,
            Foreground = new SolidColorBrush(Color.FromRgb(0xE6, 0xE8, 0xEE)),
            FontFamily = new FontFamily("Segoe UI"),
            FontSize = 28,
            FontWeight = FontWeights.SemiBold,
            TextAlignment = TextAlignment.Center,
            TextWrapping = TextWrapping.Wrap,
            MaxWidth = 900,
        });
        return panel;
    }

    private void OnDisplaysChanged(object? sender, EventArgs e) => Application.Current?.Dispatcher.BeginInvoke(Rebuild);

    private void Rebuild()
    {
        var old = _covers.ToArray();
        _covers.Clear();
        foreach (var cover in old) cover.Close();
        if (!_shown) return;

        var keepScreen = _keep is null ? (IntPtr?)null : MonitorFromWindow(new WindowInteropHelper(_keep).Handle, 2);
        foreach (var monitor in Monitors())
        {
            if (keepScreen is { } k && monitor.Handle == k) continue;
            var cover = new Window
            {
                WindowStyle = WindowStyle.None,
                ResizeMode = ResizeMode.NoResize,
                ShowInTaskbar = false,
                Topmost = true,
                Background = Brushes.Black,
                Left = monitor.Bounds.Left,
                Top = monitor.Bounds.Top,
                Width = monitor.Bounds.Width,
                Height = monitor.Bounds.Height,
                Content = Branding(),
            };
            // Координаты монитора — в пикселях, у WPF — в DIP: окно встаёт на свой монитор и разворачивается на весь его экран.
            cover.Loaded += (_, _) => cover.WindowState = WindowState.Maximized;
            cover.Closing += (_, args) => args.Cancel = _shown && _covers.Contains(cover);
            cover.Show();
            _covers.Add(cover);
        }
    }

    public void Dispose()
    {
        SystemEvents.DisplaySettingsChanged -= OnDisplaysChanged;
        _shown = false;
        foreach (var cover in _covers.ToArray())
        {
            _covers.Remove(cover);
            cover.Close();
        }
    }

    private static List<(IntPtr Handle, Rect Bounds)> Monitors()
    {
        var list = new List<(IntPtr, Rect)>();
        EnumDisplayMonitors(IntPtr.Zero, IntPtr.Zero, (IntPtr monitor, IntPtr _, ref NativeRect rect, IntPtr _) =>
        {
            list.Add((monitor, new Rect(rect.Left, rect.Top, rect.Right - rect.Left, rect.Bottom - rect.Top)));
            return true;
        }, IntPtr.Zero);
        return list;
    }

    [System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)]
    private struct NativeRect
    {
        public int Left, Top, Right, Bottom;
    }

    private delegate bool MonitorEnumProc(IntPtr monitor, IntPtr hdc, ref NativeRect rect, IntPtr data);

    [System.Runtime.InteropServices.DllImport("user32.dll")]
    [return: System.Runtime.InteropServices.MarshalAs(System.Runtime.InteropServices.UnmanagedType.Bool)]
    private static extern bool EnumDisplayMonitors(IntPtr hdc, IntPtr clip, MonitorEnumProc callback, IntPtr data);

    [System.Runtime.InteropServices.DllImport("user32.dll")]
    private static extern IntPtr MonitorFromWindow(IntPtr window, uint flags);
}
