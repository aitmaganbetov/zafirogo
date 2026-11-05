using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Health;

/// <summary>
/// Сборка <see cref="ComputerHealth"/> из источников <see cref="IHealthProbe"/> (чистый код, без вызовов ОС — проверяется
/// unit-тестами). Источник, бросивший исключение или вернувший невозможное значение, — «не измерено», остальные поля
/// заполняются. SMART и журнал сбоев дорогие — читаются не чаще <see cref="HealthCollectorOptions.SmartRefresh"/> и
/// <see cref="HealthCollectorOptions.CrashRefresh"/>. «Срочное» изменение (Status с health сразу, а не по интервалу) —
/// подключение/отключение периферии и смена SMART тома; температуры, загрузка и место ждут интервала.
/// Не потокобезопасен: вызывается последовательно из одной задачи (<see cref="HealthMonitor"/>).
/// </summary>
public sealed class HealthCollector
{
    /// <summary>Предел длины названия устройства (agent.proto, Peripheral.name).</summary>
    public const int MaxNameLength = 128;

    /// <summary>Предел длины точки монтирования (сервер: до 16 символов; длиннее — том не показывается).</summary>
    public const int MaxMountLength = 16;

    // Правдоподобные температуры: выше — сбой датчика/драйвера, «не измерено».
    private const int MaxTemperatureDeciCelsius = 1500;

    private readonly IHealthProbe _probe;
    private readonly TimeProvider _time;
    private readonly ILogger _log;
    private readonly HealthCollectorOptions _options;
    private readonly HashSet<string> _failing = new(StringComparer.Ordinal);

    // Периферия: всё, что видели с запуска службы (вид + название → подключено ли сейчас), в порядке первого появления.
    private readonly List<PeripheralEntry> _known = [];
    private readonly HashSet<PeripheralKind> _measuredKinds = [];
    private List<Peripheral>? _lastPeripherals;

    private IReadOnlyDictionary<string, DiskSmart>? _smart;
    private DateTimeOffset _smartAt;
    private CrashCounts? _crashes;
    private CrashCounts? _bootCrashes;
    // Сбои перед загрузкой больше не изменятся (загрузка давно прошла, см. BootCrashSettle) — журнал не перечитывается.
    private bool _bootCrashesFinal;
    private DateTimeOffset? _crashesAt;

    public HealthCollector(IHealthProbe probe, TimeProvider? time = null, ILogger? log = null, HealthCollectorOptions? options = null)
    {
        _probe = probe ?? throw new ArgumentNullException(nameof(probe));
        _time = time ?? TimeProvider.System;
        _log = log ?? NullLogger.Instance;
        _options = options ?? new HealthCollectorOptions();
    }

    /// <summary>Периферия по последнему опросу (для подмены в готовом снимке после <see cref="RefreshPeripherals"/>).</summary>
    public IReadOnlyList<Peripheral> CurrentPeripherals => _lastPeripherals ?? [];

    /// <summary>Полный снимок. <paramref name="urgent"/> — изменилась периферия или SMART с прошлого снимка.</summary>
    public ComputerHealth Collect(string? serverHost, out bool urgent)
    {
        var now = _time.GetUtcNow();
        var h = new ComputerHealth();

        h.CpuTempDecicelsius = Temperature(Measure("cpu_temp", _probe.CpuTemperatureDeciCelsius, 0));

        var gpu = Measure("gpu", _probe.Gpu, null);
        if (gpu is { } g)
        {
            h.GpuTempDecicelsius = Temperature(g.TemperatureDeciCelsius);
            if (g.LoadMeasured)
            {
                h.GpuMeasured = true;
                h.GpuLoadBasisPoints = Math.Min(10000u, g.LoadBasisPoints);
                h.GpuMemoryBasisPoints = Math.Min(10000u, g.MemoryBasisPoints);
            }
        }

        // Бездисковый ПК (загрузка по сети с сервера образов): локальных дисков нет — тома образа виртуальные (iSCSI,
        // RAM-диск кэша), их место и SMART ничего не говорят о ПК. Диски и SMART не читаются вовсе (S7-HA-13).
        var smartChanged = false;
        if (!_options.Diskless)
        {
            var disks = Measure("disks", _probe.Disks, []);
            if (disks.Count > _options.MaxDisks) disks = disks.Take(_options.MaxDisks).ToList();
            smartChanged = RefreshSmart(disks, now);
            foreach (var d in disks)
            {
                var mount = Clean(d.Mount);
                if (mount.Length == 0 || mount.Length > MaxMountLength) continue;
                h.Disks.Add(new DiskHealth
                {
                    Mount = mount,
                    System = d.System,
                    TotalMb = d.TotalMb,
                    FreeMb = Math.Min(d.FreeMb, d.TotalMb),
                    Smart = _smart is not null && _smart.TryGetValue(d.Mount, out var s) ? s : DiskSmart.Unspecified,
                });
            }
        }

        var peripheralsChanged = RefreshPeripherals();
        h.Peripherals.AddRange(CurrentPeripherals);

        h.LinkSpeedMbps = Measure("link_speed", () => _probe.LinkSpeedMbps(serverHost), 0u);
        h.UptimeSeconds = Measure("uptime", _probe.UptimeSeconds, 0ul);

        if (_crashesAt is null || now - _crashesAt.Value >= _options.CrashRefresh)
        {
            _crashes = Measure("crashes", () => _probe.Crashes(now - _options.CrashWindow), null);
            // Сбои перед этой загрузкой: события пишет загрузка после сбоя, поэтому окно — с начала загрузки (время работы)
            // с запасом на поправку часов при загрузке. Из такого окна события не выпадают — сервер считает по ним точный
            // прирост вместе с boot_id (agent.proto ComputerHealth.unexpected_shutdowns_boot). Время работы не измерено —
            // и это не измерено.
            // События пишутся в первые минуты загрузки, поэтому после BootCrashMargin + BootCrashSettle времени работы
            // ответ уже не изменится (окно начинается в одном и том же месте) — он запоминается до конца работы службы.
            var uptime = h.UptimeSeconds;
            if (!_bootCrashesFinal)
            {
                _bootCrashes = uptime > 0
                    ? Measure("crashes_boot", () => _probe.Crashes(now - TimeSpan.FromSeconds(uptime) - _options.BootCrashMargin), null)
                    : null;
                _bootCrashesFinal = _bootCrashes is not null &&
                    TimeSpan.FromSeconds(uptime) > _options.BootCrashMargin + _options.BootCrashSettle;
            }
            _crashesAt = now;
        }

        if (_crashes is { } c)
        {
            h.UnexpectedShutdowns7D = c.UnexpectedShutdowns;
            h.Bugchecks7D = c.BugChecks;
        }

        if (_bootCrashes is { } b)
        {
            h.CrashesBootMeasured = true;
            h.UnexpectedShutdownsBoot = b.UnexpectedShutdowns;
            h.BugchecksBoot = b.BugChecks;
        }

        urgent = peripheralsChanged || smartChanged;
        return h;
    }

    /// <summary>
    /// Опрос только периферии (дёшево, чаще полного снимка). true — изменилось подключение (новое устройство, отключение
    /// известного); первый опрос вида не считается изменением.
    /// </summary>
    public bool RefreshPeripherals()
    {
        var scan = Measure("peripherals", _probe.Peripherals, PeripheralScan.None) ?? PeripheralScan.None;
        var previouslyMeasured = new HashSet<PeripheralKind>(_measuredKinds);
        _measuredKinds.Clear();
        foreach (var k in scan.Measured ?? [])
        {
            if (k != PeripheralKind.Unspecified) _measuredKinds.Add(k);
        }

        // Устройства одного вида с одинаковым названием (несколько HID-коллекций одной клавиатуры) — одна запись.
        var present = (scan.Present ?? []).Where(p => _measuredKinds.Contains(p.Kind))
            .Select(p => (p.Kind, Name: Truncate(Clean(p.Name))))
            .Distinct()
            .ToList();
        foreach (var entry in _known.Where(e => _measuredKinds.Contains(e.Kind))) entry.Connected = false;
        foreach (var (kind, name) in present)
        {
            var entry = _known.Find(e => e.Kind == kind && e.Name == name);
            if (entry is null) _known.Add(entry = new PeripheralEntry(kind, name));
            entry.Connected = true;
        }

        Forget();

        var list = new List<Peripheral>();
        foreach (var kind in _measuredKinds.Order())
        {
            var entries = _known.Where(e => e.Kind == kind).ToList();
            if (entries.Count == 0)
            {
                list.Add(new Peripheral { Kind = kind, Connected = false });
                continue;
            }

            list.AddRange(entries.Select(e => new Peripheral { Kind = kind, Connected = e.Connected, Name = e.Name }));
        }

        if (list.Count > _options.MaxPeripherals) list = list.Take(_options.MaxPeripherals).ToList();
        var previous = _lastPeripherals;
        _lastPeripherals = list;
        if (previous is null) return false;

        // Изменение — только в видах, измеренных и в прошлый, и в этот раз (источник «ожил» — не повод слать сразу).
        static IEnumerable<(PeripheralKind, string, bool)> Of(IEnumerable<Peripheral> ps, ISet<PeripheralKind> kinds) =>
            ps.Where(p => kinds.Contains(p.Kind)).Select(p => (p.Kind, p.Name, p.Connected));
        var both = new HashSet<PeripheralKind>(previouslyMeasured);
        both.IntersectWith(_measuredKinds);
        return !Of(previous, both).ToHashSet().SetEquals(Of(list, both));
    }

    /// <summary>Отключённые устройства помнятся, пока их не больше <see cref="HealthCollectorOptions.RememberPerKind"/> на вид.</summary>
    private void Forget()
    {
        foreach (var kind in _known.Select(e => e.Kind).Distinct().ToList())
        {
            var extra = _known.Count(e => e.Kind == kind) - _options.RememberPerKind;
            if (extra <= 0) continue;
            foreach (var stale in _known.Where(e => e.Kind == kind && !e.Connected).Take(extra).ToList()) _known.Remove(stale);
        }
    }

    private bool RefreshSmart(IReadOnlyList<DiskSpace> disks, DateTimeOffset now)
    {
        if (_smart is not null && now - _smartAt < _options.SmartRefresh) return false;
        var previous = _smart;
        _smart = Measure<IReadOnlyDictionary<string, DiskSmart>>("smart", () => _probe.SmartStatus(disks), new Dictionary<string, DiskSmart>());
        _smartAt = now;
        if (previous is null) return false;
        foreach (var (mount, state) in _smart)
        {
            if (state == DiskSmart.Unspecified) continue;
            if (previous.TryGetValue(mount, out var was) && was != DiskSmart.Unspecified && was != state) return true;
        }

        return false;
    }

    private T Measure<T>(string source, Func<T> read, T fallback)
    {
        try
        {
            var v = read();
            if (_failing.Remove(source)) _log.LogInformation("health source {Source} recovered", source);
            return v;
        }
#pragma warning disable CA1031 // любой сбой источника — «не измерено», а не падение сбора
        catch (Exception ex)
#pragma warning restore CA1031
        {
            if (_failing.Add(source)) _log.LogWarning("health source {Source} failed: {Error}", source, ex.GetType().Name + ": " + ex.Message);
            return fallback;
        }
    }

    private static int Temperature(int deciCelsius) => deciCelsius is > 0 and <= MaxTemperatureDeciCelsius ? deciCelsius : 0;

    /// <summary>
    /// Текст для показа админу: управляющие символы и переключатели направления письма (подмена отображаемого имени
    /// устройства) — пробелом, пробелы схлопываются. Сервер отбрасывает health с такими символами целиком.
    /// </summary>
    public static string Clean(string? s)
    {
        if (string.IsNullOrEmpty(s)) return "";
        var chars = s.ToCharArray();
        for (var i = 0; i < chars.Length; i++)
        {
            var c = chars[i];
            if (char.IsControl(c) || c is '\u200E' or '\u200F' or (>= '\u202A' and <= '\u202E') or (>= '\u2066' and <= '\u2069')) chars[i] = ' ';
        }

        return string.Join(' ', new string(chars).Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
    }

    /// <summary>Не длиннее <see cref="MaxNameLength"/> символов, без разрезанной суррогатной пары.</summary>
    public static string Truncate(string s)
    {
        ArgumentNullException.ThrowIfNull(s);
        if (s.Length <= MaxNameLength) return s;
        var n = char.IsHighSurrogate(s[MaxNameLength - 1]) ? MaxNameLength - 1 : MaxNameLength;
        return s[..n];
    }

    private sealed class PeripheralEntry(PeripheralKind kind, string name)
    {
        public PeripheralKind Kind { get; } = kind;

        public string Name { get; } = name;

        public bool Connected { get; set; }
    }
}

/// <summary>Частота дорогих источников и пределы снимка.</summary>
public sealed class HealthCollectorOptions
{
    /// <summary>SMART (WMI MSFT_PhysicalDisk) — не чаще.</summary>
    public TimeSpan SmartRefresh { get; set; } = TimeSpan.FromMinutes(5);

    /// <summary>Журнал сбоев — не чаще.</summary>
    public TimeSpan CrashRefresh { get; set; } = TimeSpan.FromMinutes(10);

    /// <summary>Окно подсчёта сбоев (agent.proto: «за 7 суток»).</summary>
    public TimeSpan CrashWindow { get; set; } = TimeSpan.FromDays(7);

    /// <summary>
    /// Запас окна «сбои перед этой загрузкой» раньше начала загрузки (поправка часов при загрузке). Больше нельзя: сбой
    /// загрузки, прожившей меньше запаса, посчитался бы и в следующей.
    /// </summary>
    public TimeSpan BootCrashMargin { get; set; } = TimeSpan.FromMinutes(2);

    /// <summary>
    /// Сколько после <see cref="BootCrashMargin"/> ждать, прежде чем считать «сбои перед этой загрузкой» окончательными
    /// (Windows дописывает события сбоя в первые минуты загрузки): дальше журнал для них не перечитывается.
    /// </summary>
    public TimeSpan BootCrashSettle { get; set; } = TimeSpan.FromMinutes(5);

    public int MaxDisks { get; set; } = 26;

    /// <summary>
    /// Бездисковый ПК (AgentOptions.Diskless, DeviceInfo.diskless): диски и SMART не собираются, ComputerHealth.disks
    /// пуст (docs/open-questions/stage7-health-agent.md S7-HA-13).
    /// </summary>
    public bool Diskless { get; set; }

    public int MaxPeripherals { get; set; } = 16;

    /// <summary>Сколько устройств одного вида помнить (отключённые показываются с названием).</summary>
    public int RememberPerKind { get; set; } = 3;
}
