using System.Diagnostics;
using System.Globalization;
using System.Net.NetworkInformation;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Linux — заглушка для тестов и разработки: сведения о машине читаются из /proc и /sys, действия питания и установка
/// обновлений только пишутся в журнал (агент в клубах работает на Windows).
/// </summary>
public sealed class LinuxPlatform : IPlatform
{
    private readonly ILogger _log;

    public LinuxPlatform(ILogger<LinuxPlatform> log, string dataDirectory)
    {
        _log = log;
        UpdateDirectory = Path.Combine(dataDirectory, "update");
    }

    public AgentCapabilities Capabilities { get; } = new()
    {
        SelfUpdate = false,
        WakeOnLan = true,
        PowerControl = true,
        ShowMessage = true,
        ServiceMode = true,
    };

    public string UpdateDirectory { get; }

    public IHealthProbe Health { get; } = new LinuxHealthProbe();

    public DeviceInfo GetDeviceInfo(string agentVersion, bool diskless)
    {
        var (mac, ip) = NetworkSelector.Primary();
        return new DeviceInfo
        {
            HardwareUuid = ReadFirstLine("/sys/class/dmi/id/product_uuid") ?? ReadFirstLine("/etc/machine-id") ?? "",
            MacAddress = mac ?? "",
            Hostname = Environment.MachineName,
            AgentVersion = agentVersion,
            OsVersion = Environment.OSVersion.VersionString,
            CpuModel = CpuModel() ?? "",
            CpuCores = Environment.ProcessorCount,
            RamTotalMb = MemTotalMb(),
            LocalIp = ip ?? "",
            Diskless = diskless,
        };
    }

    public string GetBootId() => ReadFirstLine("/proc/sys/kernel/random/boot_id") ?? Guid.NewGuid().ToString();

    public ComputerLoad GetLoad() => new()
    {
        AgentMemoryMb = (uint)(Environment.WorkingSet / (1024 * 1024)),
    };

    public Task<bool> RebootAsync(TimeSpan delay, CancellationToken cancellationToken)
    {
        _log.LogWarning("reboot requested (delay {Delay}) — Linux stub, nothing done", delay);
        return Task.FromResult(true);
    }

    public Task<bool> ShutdownAsync(TimeSpan delay, CancellationToken cancellationToken)
    {
        _log.LogWarning("shutdown requested (delay {Delay}) — Linux stub, nothing done", delay);
        return Task.FromResult(true);
    }

    public Task<int> WakeOnLanAsync(IReadOnlyCollection<string> macAddresses, CancellationToken cancellationToken) =>
        Task.FromResult(WakeOnLanSender.Send(macAddresses, _log));

    public Task<bool> InstallUpdateAsync(string installerPath, CancellationToken cancellationToken)
    {
        _log.LogWarning("update installer {Path} verified — Linux stub, not started", installerPath);
        return Task.FromResult(false);
    }

    private static string? ReadFirstLine(string path)
    {
        try
        {
            var s = File.ReadLines(path).FirstOrDefault()?.Trim();
            return string.IsNullOrEmpty(s) ? null : s;
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            return null;
        }
    }

    private static string? CpuModel()
    {
        try
        {
            foreach (var line in File.ReadLines("/proc/cpuinfo"))
            {
                if (line.StartsWith("model name", StringComparison.Ordinal)) return line[(line.IndexOf(':', StringComparison.Ordinal) + 1)..].Trim();
            }
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
        }

        return null;
    }

    private static long MemTotalMb()
    {
        try
        {
            foreach (var line in File.ReadLines("/proc/meminfo"))
            {
                if (!line.StartsWith("MemTotal:", StringComparison.Ordinal)) continue;
                var parts = line.Split(' ', StringSplitOptions.RemoveEmptyEntries);
                return long.Parse(parts[1], CultureInfo.InvariantCulture) / 1024;
            }
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or FormatException)
        {
        }

        return 0;
    }
}

/// <summary>Выбор основного сетевого адаптера (как NetworkSelector старого агента: работает, не loopback, есть шлюз, Ethernet предпочтительнее).</summary>
public static class NetworkSelector
{
    public static (string? Mac, string? Ip) Primary()
    {
        try
        {
            var best = NetworkInterface.GetAllNetworkInterfaces()
                .Where(n => n.OperationalStatus == OperationalStatus.Up
                            && n.NetworkInterfaceType is not (NetworkInterfaceType.Loopback or NetworkInterfaceType.Tunnel)
                            && n.GetPhysicalAddress().GetAddressBytes().Length == 6)
                .Select(n => (Nic: n, Props: n.GetIPProperties()))
                .OrderByDescending(x => x.Props.GatewayAddresses.Count > 0)
                .ThenByDescending(x => x.Nic.NetworkInterfaceType == NetworkInterfaceType.Ethernet)
                .FirstOrDefault();
            if (best.Nic is null) return (null, null);
            var mac = string.Join(':', best.Nic.GetPhysicalAddress().GetAddressBytes().Select(b => b.ToString("X2", CultureInfo.InvariantCulture)));
            var ip = best.Props.UnicastAddresses.Select(a => a.Address)
                .FirstOrDefault(a => a.AddressFamily == System.Net.Sockets.AddressFamily.InterNetwork)?.ToString();
            return (mac, ip);
        }
        catch (NetworkInformationException)
        {
            return (null, null);
        }
    }
}

/// <summary>
/// Wake-on-LAN: «магический пакет» (6 байт 0xFF и 16 раз MAC) широковещательно на порты 9 и 7.
/// Перенесено из старого агента: WindowsAgent/ZafiroAgent/Infrastructure/WakeOnLan.cs.
/// </summary>
public static class WakeOnLanSender
{
    public static byte[] MagicPacket(string mac)
    {
        ArgumentNullException.ThrowIfNull(mac);
        var parts = mac.Split(':', '-');
        if (parts.Length != 6) throw new FormatException("MAC address must have six bytes.");
        var bytes = new byte[6];
        for (var i = 0; i < 6; i++) bytes[i] = byte.Parse(parts[i], NumberStyles.HexNumber, CultureInfo.InvariantCulture);
        var packet = new byte[6 + 16 * 6];
        for (var i = 0; i < 6; i++) packet[i] = 0xFF;
        for (var i = 0; i < 16; i++) Buffer.BlockCopy(bytes, 0, packet, 6 + i * 6, 6);
        return packet;
    }

    public static int Send(IReadOnlyCollection<string> macs, ILogger log)
    {
        ArgumentNullException.ThrowIfNull(macs);
        if (macs.Count == 0) return 0;
        var sent = 0;
        try
        {
            using var client = new System.Net.Sockets.UdpClient { EnableBroadcast = true };
            foreach (var mac in macs)
            {
                byte[] packet;
                try
                {
                    packet = MagicPacket(mac);
                }
                catch (FormatException)
                {
                    continue;
                }

                foreach (var port in new[] { 9, 7 }) client.Send(packet, packet.Length, new System.Net.IPEndPoint(System.Net.IPAddress.Broadcast, port));
                sent++;
            }
        }
        catch (Exception ex) when (ex is System.Net.Sockets.SocketException or ObjectDisposedException)
        {
            log.LogWarning("wake-on-lan failed: {Error}", ex.Message);
        }

        return sent;
    }
}

/// <summary>Процесс службы: память для Status.load.agent_memory_mb.</summary>
internal static class ProcessInfo
{
    public static uint WorkingSetMb()
    {
        using var p = Process.GetCurrentProcess();
        return (uint)(p.WorkingSet64 / (1024 * 1024));
    }
}
