using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Runtime;

/// <summary>
/// Тема клуба (настройки клуба → «Агент ПК») из ClubConfig.shell_theme: цвета оболочки гостя и экрана входа.
/// null — стандартная тема агента. Старый аналог: WindowsAgent/ZafiroAgent/Services/ShellThemes.cs (коммит e38b764).
/// Цвета попадают в CSS страницы, поэтому служба пропускает только "#rrggbb" (страница проверяет ещё раз).
/// </summary>
public static class ShellThemes
{
    /// <summary>
    /// Проверенная тема: все шесть цветов в формате #RRGGBB (приводятся к нижнему регистру). Нет темы, нет какого-нибудь
    /// цвета или цвет в другом формате — null (стандартная тема, как у старого агента).
    /// </summary>
    public static ShellTheme? Normalize(ShellTheme? theme)
    {
        if (theme is null) return null;
        var colors = new[] { theme.Accent, theme.Accent2, theme.Soft, theme.Highlight, theme.Bg0, theme.Bg1 };
        if (!colors.All(IsColor)) return null;
        return new ShellTheme
        {
            Accent = theme.Accent.ToLowerInvariant(),
            Accent2 = theme.Accent2.ToLowerInvariant(),
            Soft = theme.Soft.ToLowerInvariant(),
            Highlight = theme.Highlight.ToLowerInvariant(),
            Bg0 = theme.Bg0.ToLowerInvariant(),
            Bg1 = theme.Bg1.ToLowerInvariant(),
        };
    }

    /// <summary>Цвет "#rrggbb" (регистр любой).</summary>
    public static bool IsColor(string? value) =>
        value is { Length: 7 } && value[0] == '#' && !value.AsSpan(1).ContainsAnyExcept("0123456789abcdefABCDEF");

    /// <summary>Темы совпадают (обе стандартные или одинаковые цвета).</summary>
    public static bool Same(ShellTheme? a, ShellTheme? b) => a is null || b is null ? a is null && b is null : a.Equals(b);
}
