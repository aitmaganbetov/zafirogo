using System.Diagnostics;
using System.Globalization;
using System.Runtime.InteropServices;
using System.Runtime.Versioning;
using Microsoft.Extensions.Logging;
using Microsoft.Win32;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Windows: действия по командам сервера (служба под SYSTEM). Перенесено из старого агента
/// (WindowsAgent/ZafiroAgent): питание — Infrastructure/PowerControl.cs (shutdown.exe /f), Wake-on-LAN —
/// Infrastructure/WakeOnLan.cs, тихая установка — Service/AgentUpdater.cs (Arguments). Сведения о ПК — без WMI
/// (WMI/System.Management не совместим с Native AOT): SMBIOS через GetSystemFirmwareTable, реестр, NetworkInterface.
/// </summary>
[SupportedOSPlatform("windows")]
public sealed partial class WindowsPlatform : IPlatform
{
    private const string BootKey = @"SOFTWARE\Zafiro\AgentBoot";
    private readonly ILogger _log;
    private readonly object _cpuGate = new();
    private ulong _lastIdle;
    private ulong _lastTotal;

    public WindowsPlatform(ILogger<WindowsPlatform> log, string dataDirectory)
    {
        _log = log;
        UpdateDirectory = Path.Combine(dataDirectory, "update");
    }

    public AgentCapabilities Capabilities { get; } = new()
    {
        SelfUpdate = true,
        WakeOnLan = true,
        PowerControl = true,
        ShowMessage = true,
        ServiceMode = true,
    };

    public string UpdateDirectory { get; }

    public IHealthProbe Health { get; } = new WindowsHealthProbe();

    public DeviceInfo GetDeviceInfo(string agentVersion, bool diskless)
    {
        var (mac, ip) = NetworkSelector.Primary();
        return new DeviceInfo
        {
            HardwareUuid = SmbiosUuid() ?? "",
            MacAddress = mac ?? "",
            Hostname = Environment.MachineName,
            AgentVersion = agentVersion,
            OsVersion = OsCaption(),
            CpuModel = Truncate(Collapse(RegistryString(@"HARDWARE\DESCRIPTION\System\CentralProcessor\0", "ProcessorNameString"))),
            CpuCores = Environment.ProcessorCount,
            RamTotalMb = (long)Math.Round(MemoryStatus().TotalPhys / 1048576.0),
            GpuModel = Truncate(GpuName()),
            LocalIp = ip ?? "",
            Diskless = diskless,
        };
    }

    /// <summary>
    /// UUID загрузки: хранится в «летучем» разделе HKLM (Windows удаляет его при перезагрузке, перезапуск службы — нет);
    /// тот же приём, что FreshBoot старого агента (Infrastructure/FreshBoot.cs), но в HKLM службы.
    /// </summary>
    public string GetBootId()
    {
        try
        {
            using var key = Registry.LocalMachine.CreateSubKey(BootKey, writable: true, RegistryOptions.Volatile);
            if (key.GetValue("BootId") is string id && Guid.TryParse(id, out _)) return id;
            var created = Guid.NewGuid().ToString();
            key.SetValue("BootId", created, RegistryValueKind.String);
            return created;
        }
        catch (Exception ex) when (ex is UnauthorizedAccessException or System.Security.SecurityException or IOException)
        {
            _log.LogWarning("boot id marker is unavailable: {Error}", ex.GetType().Name);
            return Guid.NewGuid().ToString();
        }
    }

    public ComputerLoad GetLoad()
    {
        var mem = MemoryStatus();
        return new ComputerLoad
        {
            CpuBasisPoints = CpuBasisPoints(),
            RamBasisPoints = Math.Min(10000u, mem.MemoryLoad * 100),
            AgentMemoryMb = ProcessInfo.WorkingSetMb(),
        };
    }

    public Task<bool> RebootAsync(TimeSpan delay, CancellationToken cancellationToken) => Task.FromResult(RunShutdown(restart: true, delay));

    public Task<bool> ShutdownAsync(TimeSpan delay, CancellationToken cancellationToken) => Task.FromResult(RunShutdown(restart: false, delay));

    public Task<int> WakeOnLanAsync(IReadOnlyCollection<string> macAddresses, CancellationToken cancellationToken) =>
        Task.FromResult(WakeOnLanSender.Send(macAddresses, _log));

    /// <summary>Тихая установка (Inno Setup), установщик сам перезапускает службу.</summary>
    public Task<bool> InstallUpdateAsync(string installerPath, CancellationToken cancellationToken)
    {
        var log = Path.Combine(UpdateDirectory, "install.log");
        using var p = Process.Start(new ProcessStartInfo(installerPath,
            $"/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /MERGETASKS=\"!protect\" /LOG=\"{log}\"")
        {
            UseShellExecute = false,
            CreateNoWindow = true,
            WorkingDirectory = Path.GetDirectoryName(installerPath)!,
        });
        return Task.FromResult(p is not null);
    }

    /// <summary>shutdown.exe с /f: открытые игры не мешают перезагрузке. false — Windows не дал выключить ПК.</summary>
    private bool RunShutdown(bool restart, TimeSpan delay)
    {
        var seconds = (int)Math.Clamp(delay.TotalSeconds, 0, 600);
        try
        {
            using var process = Process.Start(new ProcessStartInfo
            {
                FileName = Path.Combine(Environment.SystemDirectory, "shutdown.exe"),
                Arguments = (restart ? "/r" : "/s") + " /f /t " + seconds.ToString(CultureInfo.InvariantCulture),
                UseShellExecute = false,
                CreateNoWindow = true,
            });
            if (process is null) return false;
            if (!process.WaitForExit(10000)) return true; // Windows уже выключается
            if (process.ExitCode == 0) return true;
            _log.LogWarning("shutdown.exe exited with code {Code}", process.ExitCode);
        }
        catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or InvalidOperationException)
        {
            _log.LogWarning("shutdown.exe failed: {Error}", ex.Message);
        }

        return false;
    }

    private uint CpuBasisPoints()
    {
        if (!GetSystemTimes(out var idle, out var kernel, out var user)) return 0;
        var total = kernel + user; // kernel включает idle
        lock (_cpuGate)
        {
            var dTotal = total - _lastTotal;
            var dIdle = idle - _lastIdle;
            _lastTotal = total;
            _lastIdle = idle;
            if (dTotal == 0 || dIdle > dTotal) return 0;
            return (uint)Math.Min(10000, (dTotal - dIdle) * 10000 / dTotal);
        }
    }

    private static string OsCaption()
    {
        var name = RegistryString(@"SOFTWARE\Microsoft\Windows NT\CurrentVersion", "ProductName");
        var display = RegistryString(@"SOFTWARE\Microsoft\Windows NT\CurrentVersion", "DisplayVersion");
        var build = RegistryString(@"SOFTWARE\Microsoft\Windows NT\CurrentVersion", "CurrentBuild");
        if (name.StartsWith("Microsoft ", StringComparison.OrdinalIgnoreCase)) name = name["Microsoft ".Length..]; // NormalizeOs старого агента
        var s = string.Join(' ', new[] { name, display }.Where(x => !string.IsNullOrEmpty(x)));
        if (!string.IsNullOrEmpty(build)) s += $" ({build})";
        return Truncate(string.IsNullOrEmpty(s) ? Environment.OSVersion.VersionString : s);
    }

    /// <summary>
    /// Все видеоадаптеры через «; » без повторов — как JoinGpuNames старого агента (Services/HardwareInfo.cs), только из
    /// реестра класса видеоадаптеров, а не WMI Win32_VideoController.
    /// </summary>
    private static string GpuName()
    {
        const string cls = @"SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}";
        var names = new List<string>();
        try
        {
            using var root = Registry.LocalMachine.OpenSubKey(cls);
            if (root is null) return "";
            foreach (var sub in root.GetSubKeyNames().Where(n => n.Length == 4 && n.All(char.IsAsciiDigit)).Order(StringComparer.Ordinal))
            {
                using var k = root.OpenSubKey(sub);
                if (k?.GetValue("DriverDesc") is string d) names.Add(Collapse(d));
            }
        }
        catch (Exception ex) when (ex is System.Security.SecurityException or UnauthorizedAccessException or IOException)
        {
        }

        return string.Join("; ", names.Where(n => n.Length > 0).Distinct(StringComparer.OrdinalIgnoreCase));
    }

    /// <summary>Пробелы схлопываются (ProcessorNameString бывает с хвостовыми пробелами) — как CollapseWhitespace старого агента.</summary>
    private static string Collapse(string s) => string.Join(' ', s.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));

    private static string RegistryString(string path, string name)
    {
        try
        {
            using var k = Registry.LocalMachine.OpenSubKey(path);
            return k?.GetValue(name) is string s ? s.Trim() : "";
        }
        catch (Exception ex) when (ex is System.Security.SecurityException or UnauthorizedAccessException or IOException)
        {
            return "";
        }
    }

    private static string Truncate(string s) => s.Length <= 255 ? s : s[..255];

    /// <summary>SMBIOS UUID (тип 1) — то же, что Win32_ComputerSystemProduct.UUID; заглушки 00…/FF… — null.</summary>
    internal static string? SmbiosUuid()
    {
        const uint rsmb = 0x52534D42; // 'RSMB'
        var size = GetSystemFirmwareTable(rsmb, 0, null, 0);
        if (size == 0) return null;
        var buf = new byte[size];
        if (GetSystemFirmwareTable(rsmb, 0, buf, size) != size) return null;
        return Smbios.ParseUuid(buf);
    }

    private static MemoryStatusEx MemoryStatus()
    {
        var m = new MemoryStatusEx { Length = (uint)Marshal.SizeOf<MemoryStatusEx>() };
        return GlobalMemoryStatusEx(ref m) ? m : default;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct MemoryStatusEx
    {
        public uint Length;
        public uint MemoryLoad;
        public ulong TotalPhys;
        public ulong AvailPhys;
        public ulong TotalPageFile;
        public ulong AvailPageFile;
        public ulong TotalVirtual;
        public ulong AvailVirtual;
        public ulong AvailExtendedVirtual;
    }

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool GlobalMemoryStatusEx(ref MemoryStatusEx buffer);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool GetSystemTimes(out ulong idle, out ulong kernel, out ulong user);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    private static partial uint GetSystemFirmwareTable(uint provider, uint tableId, [Out] byte[]? buffer, uint size);
}

/// <summary>Разбор таблицы SMBIOS (без вызовов ОС — проверяется тестами на любой платформе).</summary>
public static class Smbios
{
    /// <summary>Разбор RawSMBIOSData: заголовок 8 байт, затем структуры (тип, длина, …, строки до двойного нуля).</summary>
    public static string? ParseUuid(ReadOnlySpan<byte> raw)
    {
        if (raw.Length < 8) return null;
        var length = BitConverter.ToInt32(raw.Slice(4, 4));
        var table = raw.Slice(8, Math.Min(length, raw.Length - 8));
        var pos = 0;
        while (pos + 4 <= table.Length)
        {
            var type = table[pos];
            var len = table[pos + 1];
            if (len < 4 || pos + len > table.Length) return null;
            if (type == 1 && len >= 0x19)
            {
                var uuid = table.Slice(pos + 8, 16);
                var allZero = true;
                var allFf = true;
                foreach (var b in uuid)
                {
                    allZero &= b == 0;
                    allFf &= b == 0xFF;
                }

                return allZero || allFf ? null : new Guid(uuid).ToString().ToUpperInvariant();
            }

            if (type == 127) return null;
            // Пропуск строк: до двух нулевых байтов подряд.
            var s = pos + len;
            while (s + 1 < table.Length && (table[s] != 0 || table[s + 1] != 0)) s++;
            pos = s + 2;
        }

        return null;
    }
}
