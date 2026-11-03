using System.Globalization;
using System.Runtime.Versioning;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Здоровье ПК на Windows (служба — LocalSystem) только штатными источниками, без драйверов ядра (WinRing0 и
/// LibreHardwareMonitor запрещены — уязвимый драйвер): CPU °C — WMI root\WMI MSAcpi_ThermalZoneTemperature; GPU — NVIDIA
/// NVML (AMD/Intel — не измеряется); диски — DriveInfo (место), WMI MSFT_PhysicalDisk.HealthStatus + IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS
/// (SMART тома); мышь, клавиатура, монитор — SetupAPI, гарнитура — MMDevice; сеть — <see cref="LinkSpeed"/>; сбои — журнал
/// System (wevtapi). docs/agent-protocol.md §14.10.
/// </summary>
[SupportedOSPlatform("windows")]
public sealed class WindowsHealthProbe : IHealthProbe
{
    private static readonly TimeSpan WmiTimeout = TimeSpan.FromSeconds(10);
    private static readonly PeripheralKind[] PnpKinds = [PeripheralKind.Mouse, PeripheralKind.Keyboard, PeripheralKind.Monitor];
    private readonly LinkSpeed _link = new();
    private readonly Nvml _nvml = new();
    private readonly Dictionary<string, string> _edidNames = new(StringComparer.OrdinalIgnoreCase);

    /// <summary>Максимум по термозонам ACPI (десятые доли кельвина → десятые доли °C); многие платы зон не отдают — 0.</summary>
    public int CpuTemperatureDeciCelsius()
    {
        using var com = ComNative.Enter();
        var best = 0;
        foreach (var row in Wmi.Query(@"ROOT\WMI", "SELECT CurrentTemperature FROM MSAcpi_ThermalZoneTemperature", ["CurrentTemperature"], WmiTimeout))
        {
            if (row[0] is long k && k > 2732 && k < 2732 + 1500) best = Math.Max(best, (int)(k - 2732));
        }

        return best;
    }

    public GpuReading? Gpu() => _nvml.Read();

    public IReadOnlyList<DiskSpace> Disks()
    {
        var systemRoot = (Path.GetPathRoot(Environment.SystemDirectory) ?? "").TrimEnd('\\');
        var list = new List<DiskSpace>();
        foreach (var d in DriveInfo.GetDrives())
        {
            if (d.DriveType != DriveType.Fixed || !d.IsReady) continue;
            var mount = d.Name.TrimEnd('\\');
            list.Add(new DiskSpace(mount, string.Equals(mount, systemRoot, StringComparison.OrdinalIgnoreCase),
                (ulong)d.TotalSize / 1048576, (ulong)d.AvailableFreeSpace / 1048576));
        }

        return list;
    }

    /// <summary>MSFT_PhysicalDisk.HealthStatus по номерам дисков; том на нескольких дисках — худший из них.</summary>
    public IReadOnlyDictionary<string, DiskSmart> SmartStatus(IReadOnlyList<DiskSpace> disks)
    {
        ArgumentNullException.ThrowIfNull(disks);
        var byDisk = new Dictionary<uint, DiskSmart>();
        using (ComNative.Enter())
        {
            foreach (var row in Wmi.Query(@"ROOT\Microsoft\Windows\Storage", "SELECT DeviceId, HealthStatus FROM MSFT_PhysicalDisk",
                         ["DeviceId", "HealthStatus"], WmiTimeout))
            {
                if (row[0] is string id && uint.TryParse(id, NumberStyles.None, CultureInfo.InvariantCulture, out var n) && row[1] is long hs)
                    byDisk[n] = HealthParsing.SmartFromHealthStatus(hs);
            }
        }

        var result = new Dictionary<string, DiskSmart>(StringComparer.OrdinalIgnoreCase);
        foreach (var d in disks)
        {
            var states = VolumeDisks.Of(d.Mount).Select(n => byDisk.TryGetValue(n, out var s) ? s : DiskSmart.Unspecified);
            var worst = HealthParsing.Worst(states);
            if (worst != DiskSmart.Unspecified) result[d.Mount] = worst;
        }

        return result;
    }

    /// <summary>Каждый вид — отдельный источник: сбой гарнитуры не делает мышь «отключённой».</summary>
    public PeripheralScan Peripherals()
    {
        var measured = new List<PeripheralKind>();
        var present = new List<PeripheralDevice>();
        foreach (var kind in PnpKinds)
        {
            try
            {
                var (cls, prefix) = kind switch
                {
                    PeripheralKind.Mouse => (PnpDevices.MouseClass, @"HID\"),
                    PeripheralKind.Keyboard => (PnpDevices.KeyboardClass, @"HID\"),
                    _ => (PnpDevices.MonitorClass, @"DISPLAY\"),
                };
                present.AddRange(PnpDevices.Present(kind, cls, prefix, _edidNames));
                measured.Add(kind);
            }
            catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or System.Runtime.InteropServices.ExternalException or DllNotFoundException or EntryPointNotFoundException)
            {
            }
        }

        try
        {
            present.AddRange(AudioEndpoints.Headsets());
            measured.Add(PeripheralKind.Headset);
        }
        catch (Exception ex) when (ex is System.Runtime.InteropServices.ExternalException or DllNotFoundException or EntryPointNotFoundException)
        {
        }

        return new PeripheralScan(measured, present);
    }

    public uint LinkSpeedMbps(string? serverHost) => _link.ForServer(serverHost);

    public ulong UptimeSeconds() => (ulong)(Environment.TickCount64 / 1000);

    public CrashCounts? Crashes(DateTimeOffset since)
    {
        var window = DateTimeOffset.UtcNow - since;
        var kernelPower = SystemEventLog.Count(HealthParsing.EventQuery("Microsoft-Windows-Kernel-Power", 41, window));
        var dirty = SystemEventLog.Count(HealthParsing.EventQuery("EventLog", 6008, window));
        var bugChecks = SystemEventLog.Count(HealthParsing.EventQuery("Microsoft-Windows-WER-SystemErrorReporting", 1001, window));
        return new CrashCounts(HealthParsing.UnexpectedShutdowns(kernelPower, dirty), bugChecks);
    }
}
