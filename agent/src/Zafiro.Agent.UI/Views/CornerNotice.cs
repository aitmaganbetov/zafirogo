using System.Media;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Threading;

namespace Zafiro.Agent.UI.Views;

/// <summary>
/// Уведомления в правом верхнем углу экрана поверх игр (без WebView2): время сессии, ответы клуба в чате, сообщения
/// сотрудника. Окно без фокуса — игра не сворачивается; звук Windows. Новое уведомление того же вида заменяет прежнее.
/// Закрываются щелчком или сами. Перенесено из старого агента: Views/CornerNotice.cs.
/// </summary>
internal sealed class CornerNotice : Window
{
    public enum Kind { SessionTime, SessionOver, Chat, StaffMessage }

    private const double EdgeMargin = 24;
    private const double Gap = 12;
    private static readonly List<CornerNotice> Open = [];

    private readonly Kind _kind;
    private readonly DispatcherTimer _closeTimer;

    /// <summary>На экране есть уведомления (процесс UI не завершается, пока они видны).</summary>
    public static bool AnyOpen => Open.Count > 0;

    public static void Show(Kind kind, string? title, string text, TimeSpan? displayFor = null)
    {
        if (string.IsNullOrWhiteSpace(text)) return;
        foreach (var same in Open.ToArray())
            if (same._kind == kind) same.Close();

        var notice = new CornerNotice(kind, title, text, displayFor);
        Open.Add(notice);
        notice.Show();
        try
        {
            (kind switch
            {
                Kind.SessionOver => SystemSounds.Hand,
                Kind.SessionTime => SystemSounds.Exclamation,
                _ => SystemSounds.Asterisk,
            }).Play();
        }
        catch (InvalidOperationException ex)
        {
            UiLog.Write("notice sound failed: " + ex.GetType().Name);
        }
    }

    private static void Layout()
    {
        var area = SystemParameters.WorkArea;
        var top = area.Top + EdgeMargin;
        foreach (var notice in Open)
        {
            if (!notice.IsLoaded) continue;
            notice.Left = area.Right - notice.ActualWidth - EdgeMargin;
            notice.Top = top;
            top += notice.ActualHeight + Gap;
        }
    }

    private CornerNotice(Kind kind, string? title, string text, TimeSpan? displayFor)
    {
        _kind = kind;
        WindowStyle = WindowStyle.None;
        AllowsTransparency = true;
        Background = Brushes.Transparent;
        ResizeMode = ResizeMode.NoResize;
        SizeToContent = SizeToContent.WidthAndHeight;
        Topmost = true;
        ShowInTaskbar = false;
        ShowActivated = false;
        Focusable = false;

        var accent = kind switch
        {
            Kind.SessionOver => Color.FromRgb(0xD9, 0x00, 0x58),
            Kind.SessionTime => Color.FromRgb(0x75, 0x54, 0xC8),
            Kind.Chat => Color.FromRgb(0x16, 0xA3, 0x4A),
            _ => Color.FromRgb(0x0E, 0x74, 0x90),
        };
        var content = new StackPanel();
        if (!string.IsNullOrWhiteSpace(title))
        {
            content.Children.Add(new TextBlock
            {
                Text = title,
                Foreground = new SolidColorBrush(Color.FromRgb(0xC4, 0xB5, 0xFD)),
                FontFamily = new FontFamily("Segoe UI"),
                FontSize = 14,
                FontWeight = FontWeights.SemiBold,
                MaxWidth = 440,
                TextTrimming = TextTrimming.CharacterEllipsis,
                Margin = new Thickness(0, 0, 0, 6),
            });
        }

        content.Children.Add(new TextBlock
        {
            Text = text,
            Foreground = Brushes.White,
            FontFamily = new FontFamily("Segoe UI"),
            FontSize = 20,
            FontWeight = FontWeights.SemiBold,
            MaxWidth = 440,
            TextWrapping = TextWrapping.Wrap,
        });
        Content = new Border
        {
            Background = new SolidColorBrush(Color.FromArgb(0xEE, 0x1B, 0x19, 0x25)),
            BorderBrush = new SolidColorBrush(accent),
            BorderThickness = new Thickness(2),
            CornerRadius = new CornerRadius(12),
            Padding = new Thickness(22, 16, 22, 16),
            Child = content,
        };

        Loaded += (_, _) => Layout();
        MouseLeftButtonUp += (_, _) => Close();
        var seconds = kind switch { Kind.SessionTime => 7, Kind.SessionOver => 10, Kind.Chat => 15, _ => 30 };
        // Срок показа сообщения сотрудника — из команды (ShowMessage.display_for), не больше 5 минут.
        if (kind == Kind.StaffMessage && displayFor is { } d && d > TimeSpan.Zero) seconds = (int)Math.Min(300, Math.Ceiling(d.TotalSeconds));
        _closeTimer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(seconds) };
        _closeTimer.Tick += (_, _) => Close();
        _closeTimer.Start();
        Closed += (_, _) =>
        {
            _closeTimer.Stop();
            Open.Remove(this);
            Layout();
        };
    }
}
