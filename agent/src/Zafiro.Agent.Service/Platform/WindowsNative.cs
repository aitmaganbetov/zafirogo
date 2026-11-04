using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Runtime.Versioning;
using System.Security.Principal;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Вызовы Win32 службы (source-generated P/Invoke — совместимо с Native AOT). Перенесено из старого агента:
/// WindowsAgent/ZafiroAgent/Service/Watchdog.cs (запуск в сеансе пользователя) и Service/VaultProtocol.cs
/// (сеанс и процесс клиента канала).
/// </summary>
[SupportedOSPlatform("windows")]
internal static unsafe partial class WindowsNative
{
    public const uint NoSession = 0xFFFFFFFF;
    private const uint ProcessQueryLimitedInformation = 0x1000;
    private const uint MaximumAllowed = 0x02000000;
    private const int SecurityImpersonation = 2;
    private const int TokenPrimary = 1;
    private const uint CreateUnicodeEnvironment = 0x00000400;
    private const int TokenElevationType = 18;
    private const int ElevationTypeDefault = 1;

    [LibraryImport("kernel32.dll")]
    public static partial uint WTSGetActiveConsoleSessionId();

    [LibraryImport("wtsapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool WTSQueryUserToken(uint sessionId, out nint token);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool GetNamedPipeClientSessionId(nint pipe, out uint sessionId);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool GetNamedPipeClientProcessId(nint pipe, out uint processId);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    private static partial nint OpenProcess(uint access, [MarshalAs(UnmanagedType.Bool)] bool inherit, uint processId);

    [LibraryImport("kernel32.dll", SetLastError = true, EntryPoint = "QueryFullProcessImageNameW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool QueryFullProcessImageName(nint process, int flags, char* path, ref int size);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool CloseHandle(nint handle);

    [LibraryImport("advapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DuplicateTokenEx(nint token, uint access, nint attributes, int level, int type, out nint newToken);

    [LibraryImport("userenv.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CreateEnvironmentBlock(out nint environment, nint token, [MarshalAs(UnmanagedType.Bool)] bool inherit);

    [LibraryImport("userenv.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DestroyEnvironmentBlock(nint environment);

    [LibraryImport("advapi32.dll", SetLastError = true, EntryPoint = "CreateProcessAsUserW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CreateProcessAsUser(nint token, char* application, char* commandLine, nint processAttributes,
        nint threadAttributes, [MarshalAs(UnmanagedType.Bool)] bool inheritHandles, uint flags, nint environment, char* directory,
        StartupInfo* startup, ProcessInformation* process);

    [LibraryImport("advapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool GetTokenInformation(nint token, int infoClass, out int info, int length, out int returnLength);

    [StructLayout(LayoutKind.Sequential)]
    private struct StartupInfo
    {
        public int Cb;
        public char* Reserved;
        public char* Desktop;
        public char* Title;
        public int X, Y, XSize, YSize, XCountChars, YCountChars, FillAttribute, Flags;
        public short ShowWindow, CbReserved2;
        public nint Reserved2, StdInput, StdOutput, StdError;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ProcessInformation
    {
        public nint Process, Thread;
        public int ProcessId, ThreadId;
    }

    /// <summary>Полный путь exe процесса; null — процесс недоступен.</summary>
    public static string? ProcessImagePath(uint pid)
    {
        var handle = OpenProcess(ProcessQueryLimitedInformation, false, pid);
        if (handle == 0) return null;
        try
        {
            var buffer = new char[1024];
            var size = buffer.Length;
            fixed (char* p = buffer)
            {
                return QueryFullProcessImageName(handle, 0, p, ref size) ? new string(buffer, 0, size) : null;
            }
        }
        finally
        {
            CloseHandle(handle);
        }
    }

    /// <summary>SID пользователя, вошедшего в сеанс; null — никто не вошёл.</summary>
    public static SecurityIdentifier? SessionUser(uint session)
    {
        if (!WTSQueryUserToken(session, out var token)) return null;
        try
        {
            using var id = new WindowsIdentity(token);
            return id.User;
        }
        finally
        {
            CloseHandle(token);
        }
    }

    /// <summary>Пользователь сеанса — администратор (UAC: разделённый токен; без UAC — группа Администраторы).</summary>
    public static bool IsAdministrator(nint token)
    {
        if (GetTokenInformation(token, TokenElevationType, out var type, sizeof(int), out _) && type != ElevationTypeDefault) return true;
        using var identity = new WindowsIdentity(token);
        return new WindowsPrincipal(identity).IsInRole(WindowsBuiltInRole.Administrator);
    }

    /// <summary>Запускает <paramref name="exe"/> в сеансе пользователя <paramref name="userToken"/> на рабочем столе winsta0\default.</summary>
    public static int LaunchAsUser(nint userToken, string exe, string arguments)
    {
        if (!DuplicateTokenEx(userToken, MaximumAllowed, 0, SecurityImpersonation, TokenPrimary, out var primary))
            throw new Win32Exception(Marshal.GetLastPInvokeError());
        nint environment = 0;
        try
        {
            if (!CreateEnvironmentBlock(out environment, primary, false)) throw new Win32Exception(Marshal.GetLastPInvokeError());
            var commandLine = ($"\"{exe}\" {arguments}" + "\0").ToCharArray(); // CreateProcessW может изменить буфер
            var dir = Path.GetDirectoryName(exe) ?? Environment.SystemDirectory;
            fixed (char* desktop = "winsta0\\default")
            fixed (char* app = exe)
            fixed (char* cmd = commandLine)
            fixed (char* cwd = dir)
            {
                var si = new StartupInfo { Cb = sizeof(StartupInfo), Desktop = desktop };
                ProcessInformation pi;
                if (!CreateProcessAsUser(primary, app, cmd, 0, 0, false, CreateUnicodeEnvironment, environment, cwd, &si, &pi))
                    throw new Win32Exception(Marshal.GetLastPInvokeError());
                CloseHandle(pi.Thread);
                CloseHandle(pi.Process);
                return pi.ProcessId;
            }
        }
        finally
        {
            if (environment != 0) DestroyEnvironmentBlock(environment);
            CloseHandle(primary);
        }
    }
}
