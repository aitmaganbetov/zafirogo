using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Threading;

namespace Zafiro.Agent.UI.Kiosk;

/// <summary>
/// Системный перехват клавиатуры (WH_KEYBOARD_LL) для экрана блокировки. Что блокируется — <see cref="KioskRules"/>.
/// Ctrl+Alt+P — PIN сотрудника (страница показывает окно PIN, служба проверяет его на сервере). Перехват работает
/// в своём потоке с циклом сообщений: если окно UI подвиснет, Windows его не снимет.
/// Перенесено из старого агента: Kiosk/KioskKeyboard.cs (без изменений логики).
/// </summary>
internal sealed class KioskKeyboard : IDisposable
{
    private const int WhKeyboardLl = 13;
    private const int WmKeyDown = 0x0100, WmSysKeyDown = 0x0104;
    private const int LlkhfAltDown = 0x20;
    private const int VkShift = 0x10, VkControl = 0x11, VkMenu = 0x12;
    private const int WmQuit = 0x0012;
    private static readonly IntPtr Blocked = new(1);

    private readonly LowLevelKeyboardProc _callback;
    private IntPtr _hook = IntPtr.Zero;
    private Thread? _thread;
    private uint _threadId;
    private volatile bool _enabled;
    private volatile bool _strict = true;

    public KioskKeyboard()
    {
        _callback = Callback; // делегат хранится в поле, иначе его соберёт GC
    }

    /// <summary>Перехват включён (виден экран блокировки).</summary>
    public bool Enabled { get => _enabled; set => _enabled = value; }

    /// <summary>Нет оплаченной сессии: блокируются все клавиши выхода.</summary>
    public bool Strict { get => _strict; set => _strict = value; }

    /// <summary>Ctrl+Alt+P (вызывается из потока перехвата).</summary>
    public event Action? StaffShortcut;

    public void Install()
    {
        if (_thread is not null) return;
        using var ready = new ManualResetEventSlim();
        _thread = new Thread(() =>
        {
            _threadId = GetCurrentThreadId();
            using (var module = Process.GetCurrentProcess().MainModule)
                _hook = SetWindowsHookEx(WhKeyboardLl, _callback, GetModuleHandle(module?.ModuleName), 0);
            if (_hook == IntPtr.Zero) UiLog.Write("keyboard hook failed: " + Marshal.GetLastWin32Error());
            ready.Set();

            while (GetMessage(out var message, IntPtr.Zero, 0, 0) > 0)
            {
                TranslateMessage(ref message);
                DispatchMessage(ref message);
            }

            if (_hook != IntPtr.Zero) UnhookWindowsHookEx(_hook);
            _hook = IntPtr.Zero;
        })
        {
            IsBackground = true,
            Priority = ThreadPriority.Highest,
            Name = "Kiosk keyboard",
        };
        _thread.Start();
        ready.Wait(TimeSpan.FromSeconds(5));
    }

    private IntPtr Callback(int nCode, IntPtr wParam, IntPtr lParam)
    {
        if (nCode >= 0)
        {
            var info = Marshal.PtrToStructure<KbdLlHookStruct>(lParam);
            var down = (int)wParam is WmKeyDown or WmSysKeyDown;
            var vk = (int)info.VkCode;
            // При зажатом Ctrl Windows может не ставить флаг Alt (Ctrl+Alt = AltGr): проверяем и саму клавишу.
            var alt = (info.Flags & LlkhfAltDown) != 0 || (GetAsyncKeyState(VkMenu) & 0x8000) != 0;
            var ctrl = (GetAsyncKeyState(VkControl) & 0x8000) != 0;
            var shift = (GetAsyncKeyState(VkShift) & 0x8000) != 0;
            var win = ((GetAsyncKeyState(KioskRules.VkLWin) | GetAsyncKeyState(KioskRules.VkRWin)) & 0x8000) != 0;

            if (KioskRules.IsStaffShortcut(vk, alt, ctrl))
            {
                if (down) StaffShortcut?.Invoke();
                return Blocked;
            }

            if (_enabled && KioskRules.ShouldBlock(vk, alt, ctrl, shift, _strict, win)) return Blocked;
        }

        return CallNextHookEx(_hook, nCode, wParam, lParam);
    }

    public void Dispose()
    {
        if (_thread is null) return;
        PostThreadMessage(_threadId, WmQuit, IntPtr.Zero, IntPtr.Zero);
        _thread.Join(TimeSpan.FromSeconds(2));
        _thread = null;
    }

    private delegate IntPtr LowLevelKeyboardProc(int nCode, IntPtr wParam, IntPtr lParam);

    [StructLayout(LayoutKind.Sequential)]
    private struct KbdLlHookStruct
    {
        public uint VkCode;
        public uint ScanCode;
        public uint Flags;
        public uint Time;
        public IntPtr ExtraInfo;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct Msg
    {
        public IntPtr Hwnd;
        public uint Message;
        public IntPtr WParam, LParam;
        public uint Time;
        public int X, Y;
    }

    [DllImport("user32.dll", SetLastError = true)]
    private static extern IntPtr SetWindowsHookEx(int idHook, LowLevelKeyboardProc lpfn, IntPtr hMod, uint dwThreadId);

    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool UnhookWindowsHookEx(IntPtr hhk);

    [DllImport("user32.dll")]
    private static extern IntPtr CallNextHookEx(IntPtr hhk, int nCode, IntPtr wParam, IntPtr lParam);

    [DllImport("user32.dll")]
    private static extern short GetAsyncKeyState(int vKey);

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode)]
    private static extern IntPtr GetModuleHandle(string? lpModuleName);

    [DllImport("user32.dll")]
    private static extern int GetMessage(out Msg message, IntPtr window, uint filterMin, uint filterMax);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool TranslateMessage(ref Msg message);

    [DllImport("user32.dll")]
    private static extern IntPtr DispatchMessage(ref Msg message);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool PostThreadMessage(uint threadId, uint message, IntPtr wParam, IntPtr lParam);

    [DllImport("kernel32.dll")]
    private static extern uint GetCurrentThreadId();
}
