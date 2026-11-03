using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Health;

/// <summary>
/// Источники здоровья ПК (Status.health, docs/agent-protocol.md §14.10). Windows — штатные источники без драйверов ядра
/// (WMI, NVML, SetupAPI, MMDevice, журнал System), Linux и тесты — свои реализации. Каждый метод — отдельный источник:
/// исключение или «нет данных» в одном не мешает остальным (<see cref="HealthCollector"/> ставит «не измерено»).
/// Методы синхронные и вызываются только из фоновой задачи сбора (<see cref="HealthMonitor"/>), не из потока Status.
/// </summary>
public interface IHealthProbe
{
    /// <summary>Температура CPU, десятые доли °C; 0 — не измерено.</summary>
    int CpuTemperatureDeciCelsius();

    /// <summary>Видеокарта; null — не измерено.</summary>
    GpuReading? Gpu();

    /// <summary>Локальные фиксированные тома: место.</summary>
    IReadOnlyList<DiskSpace> Disks();

    /// <summary>SMART томов (ключ — <see cref="DiskSpace.Mount"/>); тома нет в словаре — не измерено.</summary>
    IReadOnlyDictionary<string, DiskSmart> SmartStatus(IReadOnlyList<DiskSpace> disks);

    /// <summary>
    /// Периферия: какие виды удалось проверить и какие устройства этих видов подключены сейчас. Вид, который не удалось
    /// проверить (сбой источника), в <see cref="PeripheralScan.Measured"/> не входит — это «не измерено», а не «отключено».
    /// </summary>
    PeripheralScan Peripherals();

    /// <summary>Скорость интерфейса, через который идёт связь с <paramref name="serverHost"/>, Мбит/с; 0 — не измерено.</summary>
    uint LinkSpeedMbps(string? serverHost);

    /// <summary>Сколько секунд ОС работает с последней загрузки.</summary>
    ulong UptimeSeconds();

    /// <summary>Сбои в журнале System с момента <paramref name="since"/>; null — не измерено.</summary>
    CrashCounts? Crashes(DateTimeOffset since);
}

/// <summary>Показания видеокарты. Загрузка и память — только при <see cref="LoadMeasured"/>.</summary>
public readonly record struct GpuReading(int TemperatureDeciCelsius, uint LoadBasisPoints, uint MemoryBasisPoints, bool LoadMeasured);

/// <summary>Том: буква («C:») или точка монтирования, системный ли, объём и свободное место.</summary>
public sealed record DiskSpace(string Mount, bool System, ulong TotalMb, ulong FreeMb);

/// <summary>Подключённое устройство: вид и название из ОС.</summary>
public readonly record struct PeripheralDevice(PeripheralKind Kind, string Name);

/// <summary>Результат опроса периферии: проверенные виды и подключённые устройства.</summary>
public sealed record PeripheralScan(IReadOnlyCollection<PeripheralKind> Measured, IReadOnlyList<PeripheralDevice> Present)
{
    public static readonly PeripheralScan None = new([], []);
}

/// <summary>Сбои за окно: внезапные выключения (Kernel-Power 41 / EventLog 6008) и «синие экраны» (BugCheck 1001).</summary>
public readonly record struct CrashCounts(uint UnexpectedShutdowns, uint BugChecks);

/// <summary>Ничего не измеряет (платформа без источников).</summary>
public sealed class NullHealthProbe : IHealthProbe
{
    public static readonly NullHealthProbe Instance = new();

    public int CpuTemperatureDeciCelsius() => 0;

    public GpuReading? Gpu() => null;

    public IReadOnlyList<DiskSpace> Disks() => [];

    public IReadOnlyDictionary<string, DiskSmart> SmartStatus(IReadOnlyList<DiskSpace> disks) => new Dictionary<string, DiskSmart>();

    public PeripheralScan Peripherals() => PeripheralScan.None;

    public uint LinkSpeedMbps(string? serverHost) => 0;

    public ulong UptimeSeconds() => (ulong)(Environment.TickCount64 / 1000);

    public CrashCounts? Crashes(DateTimeOffset since) => null;
}
