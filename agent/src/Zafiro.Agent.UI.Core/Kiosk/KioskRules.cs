namespace Zafiro.Agent.UI.Kiosk;

/// <summary>
/// Какие сочетания клавиш перехватывает экран блокировки (WH_KEYBOARD_LL, сам перехват — Zafiro.Agent.UI KioskKeyboard).
/// Ctrl+Alt+P всегда открывает PIN сотрудника. Пока ПК не оплачен (strict) блокируются клавиши, которые выводят из
/// агента: Win, Alt+Tab, Alt+Esc, Ctrl+Esc, Alt+F4, Alt+Пробел, клавиша меню. Всегда — Ctrl+Shift+Esc (диспетчер
/// задач), Win+Tab и Win+Ctrl+D/←/→/F4 (виртуальные рабочие столы). Ctrl+Alt+Del перехватить нельзя.
/// Перенесено без изменений из старого агента: Kiosk/KioskKeyboard.cs ShouldBlock(), IsStaffShortcut().
/// </summary>
public static class KioskRules
{
    public const int VkTab = 0x09, VkEscape = 0x1B, VkSpace = 0x20, VkP = 0x50, VkF4 = 0x73;
    public const int VkLWin = 0x5B, VkRWin = 0x5C, VkApps = 0x5D;
    public const int VkLeft = 0x25, VkRight = 0x27, VkD = 0x44;

    public static bool ShouldBlock(int vk, bool alt, bool ctrl, bool shift, bool strict, bool win = false)
    {
        if (ctrl && shift && vk == VkEscape) return true;
        if (win && (vk == VkTab || (ctrl && vk is VkD or VkLeft or VkRight or VkF4))) return true;
        if (!strict) return false;

        return vk is VkLWin or VkRWin or VkApps
            || (alt && vk is VkTab or VkEscape or VkF4 or VkSpace)
            || (ctrl && vk == VkEscape);
    }

    public static bool IsStaffShortcut(int vk, bool alt, bool ctrl) => ctrl && alt && vk == VkP;
}
