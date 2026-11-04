using Zafiro.Agent.Core.Health;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Linux (стенд разработки и тесты): место на дисках, аптайм, скорость сети. Температуры, SMART, периферия и журнал
/// сбоев — «не измерено» (агент в клубах работает на Windows).
/// </summary>
public sealed class LinuxHealthProbe : IHealthProbe
{
    private readonly LinkSpeed _link = new();

    public int CpuTemperatureDeciCelsius() => 0;

    public GpuReading? Gpu() => null;

    public IReadOnlyList<DiskSpace> Disks() => DriveInfo.GetDrives()
        .Where(d => d.DriveType == DriveType.Fixed && d.IsReady && d.TotalSize > 0)
        .Select(d => new DiskSpace(d.Name, d.Name == "/", (ulong)d.TotalSize / 1048576, (ulong)d.AvailableFreeSpace / 1048576))
        .ToList();

    public IReadOnlyDictionary<string, DiskSmart> SmartStatus(IReadOnlyList<DiskSpace> disks) => new Dictionary<string, DiskSmart>();

    public PeripheralScan Peripherals() => PeripheralScan.None;

    public uint LinkSpeedMbps(string? serverHost) => _link.ForServer(serverHost);

    public ulong UptimeSeconds() => (ulong)(Environment.TickCount64 / 1000);

    public CrashCounts? Crashes(DateTimeOffset since) => null;
}
