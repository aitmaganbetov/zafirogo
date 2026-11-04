using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.Service.Platform;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>Здоровье ПК (Status.health): сборка снимка, «не измерено», срочные изменения, когда класть health в Status.</summary>
public sealed class HealthTests : IDisposable
{
    private static readonly DateTimeOffset T0 = new(2026, 11, 3, 12, 0, 0, TimeSpan.Zero);
    private readonly ManualClock _clock = new(T0);
    private readonly FakeHealthProbe _probe = new();
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private HealthCollector Collector() => new(_probe, _clock);

    private static Peripheral Find(ComputerHealth h, PeripheralKind kind) => Assert.Single(h.Peripherals, p => p.Kind == kind);

    // ------------------------------------------------------------------ сборка снимка

    [Fact]
    public void Снимок_заполняет_все_поля_из_источников()
    {
        var h = Collector().Collect("agent.example", out var urgent);
        Assert.False(urgent);
        Assert.Equal(650, h.CpuTempDecicelsius);
        Assert.Equal(710, h.GpuTempDecicelsius);
        Assert.True(h.GpuMeasured);
        Assert.Equal(4200u, h.GpuLoadBasisPoints);
        Assert.Equal(3100u, h.GpuMemoryBasisPoints);
        Assert.Equal(2, h.Disks.Count);
        var c = h.Disks[0];
        Assert.Equal("C:", c.Mount);
        Assert.True(c.System);
        Assert.Equal(476_000ul, c.TotalMb);
        Assert.Equal(120_000ul, c.FreeMb);
        Assert.Equal(DiskSmart.Ok, c.Smart);
        Assert.Equal(4, h.Peripherals.Count);
        Assert.All(h.Peripherals, p => Assert.True(p.Connected));
        Assert.Equal("Logitech G102", Find(h, PeripheralKind.Mouse).Name);
        Assert.Equal(1000u, h.LinkSpeedMbps);
        Assert.Equal(3600ul, h.UptimeSeconds);
        Assert.Equal(2u, h.UnexpectedShutdowns7D);
        Assert.Equal(1u, h.Bugchecks7D);
    }

    [Fact]
    public void Бездисковый_ПК_диски_и_SMART_не_собираются()
    {
        var c = new HealthCollector(_probe, _clock, options: new HealthCollectorOptions { Diskless = true });
        for (var i = 0; i < 3; i++)
        {
            var h = c.Collect("agent.example", out var urgent);
            Assert.False(urgent);
            Assert.Empty(h.Disks);
            Assert.Equal(650, h.CpuTempDecicelsius); // остальное — как обычно
            Assert.Equal(1000u, h.LinkSpeedMbps);
            Assert.Equal(4, h.Peripherals.Count);
            _clock.Advance(TimeSpan.FromMinutes(6)); // дольше SmartRefresh
        }

        Assert.Equal(0, _probe.DiskCalls);
        Assert.Equal(0, _probe.SmartCalls);
    }

    [Fact]
    public async Task Runtime_бездискового_ПК_не_читает_диски()
    {
        var platform = new FakePlatform();
        var options = new AgentOptions { EnrollEndpoint = "localhost:1", Diskless = true };
        await using var rt = Runtimes.Create(_dir.Path, platform, _pki, time: _clock, options: options);
        await rt.Health.TickAsync(CancellationToken.None);
        var st = rt.NextStatus();
        Assert.NotNull(st.Health);
        Assert.Empty(st.Health.Disks);
        Assert.Equal(0, platform.FakeHealth.DiskCalls);
        Assert.Equal(0, platform.FakeHealth.SmartCalls);
    }

    [Fact]
    public void Источник_бросил_исключение_поле_не_измерено_остальные_есть()
    {
        _probe.CpuFails = new InvalidOperationException("WBEM_E_NOT_SUPPORTED");
        _probe.GpuFails = new DllNotFoundException("nvml.dll");
        var h = Collector().Collect(null, out _);
        Assert.Equal(0, h.CpuTempDecicelsius);
        Assert.Equal(0, h.GpuTempDecicelsius);
        Assert.False(h.GpuMeasured);
        Assert.Equal(0u, h.GpuLoadBasisPoints);
        Assert.Equal(2, h.Disks.Count);
        Assert.Equal(4, h.Peripherals.Count);
        Assert.Equal(1000u, h.LinkSpeedMbps);
    }

    [Fact]
    public void Сбой_опроса_периферии_не_отключение_а_не_измерено()
    {
        var collector = Collector();
        collector.Collect(null, out _);
        _probe.PeripheralsFail = new System.ComponentModel.Win32Exception(5);
        Assert.False(collector.RefreshPeripherals()); // не «мышь отключена»
        var h = collector.Collect(null, out var urgent);
        Assert.False(urgent);
        Assert.Empty(h.Peripherals);

        _probe.PeripheralsFail = null;
        Assert.False(collector.RefreshPeripherals()); // источник ожил — не срочно
        Assert.Equal(4, collector.CurrentPeripherals.Count);
    }

    [Fact]
    public void Невозможная_температура_не_измерено()
    {
        _probe.CpuTemp = 2731; // «273 °C» — сбой датчика
        Assert.Equal(0, Collector().Collect(null, out _).CpuTempDecicelsius);
        _probe.CpuTemp = -50;
        Assert.Equal(0, Collector().Collect(null, out _).CpuTempDecicelsius);
    }

    [Fact]
    public void Название_обрезается_до_128_символов_без_разрыва_суррогатной_пары()
    {
        Assert.Equal(128, HealthCollector.Truncate(new string('м', 300)).Length);
        var s = new string('a', 127) + "😀" + "tail";
        var cut = HealthCollector.Truncate(s);
        Assert.Equal(127, cut.Length);
        Assert.False(char.IsHighSurrogate(cut[^1]));

        Assert.Equal("evil gpj.exe", HealthCollector.Clean("evil\u202Egpj.exe"));
        Assert.Equal("a b", HealthCollector.Clean("a\t\r\n b\u0000"));

        _probe.Plug(PeripheralKind.Mouse, "  Очень   " + new string('x', 200));
        var h = Collector().Collect(null, out _);
        Assert.All(h.Peripherals, p => Assert.True(p.Name.Length <= 128));
        Assert.Contains(h.Peripherals, p => p.Name.StartsWith("Очень x", StringComparison.Ordinal));
    }

    // ------------------------------------------------------------------ срочные изменения

    [Fact]
    public void Отключение_мыши_срочно_и_название_остаётся()
    {
        var collector = Collector();
        collector.Collect(null, out _);
        Assert.False(collector.RefreshPeripherals()); // ничего не изменилось

        _probe.Unplug(PeripheralKind.Mouse);
        Assert.True(collector.RefreshPeripherals());
        var mouse = Assert.Single(collector.CurrentPeripherals, p => p.Kind == PeripheralKind.Mouse);
        Assert.False(mouse.Connected);
        Assert.Equal("Logitech G102", mouse.Name);
        Assert.False(collector.RefreshPeripherals());

        _probe.Plug(PeripheralKind.Mouse, "Logitech G102");
        Assert.True(collector.RefreshPeripherals());
        Assert.True(Assert.Single(collector.CurrentPeripherals, p => p.Kind == PeripheralKind.Mouse).Connected);
    }

    [Fact]
    public void Гарнитуры_не_было_с_запуска_вид_есть_отключён_без_названия()
    {
        _probe.Unplug(PeripheralKind.Headset);
        var collector = Collector();
        var h = collector.Collect(null, out _);
        var headset = Find(h, PeripheralKind.Headset);
        Assert.False(headset.Connected);
        Assert.Equal("", headset.Name);

        _probe.Plug(PeripheralKind.Headset, "Headphones (Realtek(R) Audio)");
        Assert.True(collector.RefreshPeripherals());
        Assert.True(Assert.Single(collector.CurrentPeripherals, p => p.Kind == PeripheralKind.Headset).Connected);
    }

    [Fact]
    public void Несколько_коллекций_одной_клавиатуры_одна_запись()
    {
        _probe.Plug(PeripheralKind.Keyboard, "HyperX Alloy");
        _probe.Plug(PeripheralKind.Keyboard, "HyperX  Alloy ");
        var h = Collector().Collect(null, out _);
        Assert.Single(h.Peripherals, p => p.Kind == PeripheralKind.Keyboard);
    }

    [Fact]
    public void Температура_изменилась_не_срочно()
    {
        var collector = Collector();
        collector.Collect(null, out _);
        _probe.CpuTemp = 950;
        _clock.Advance(TimeSpan.FromSeconds(60));
        var h = collector.Collect(null, out var urgent);
        Assert.False(urgent);
        Assert.Equal(950, h.CpuTempDecicelsius);
    }

    [Fact]
    public void Смена_SMART_срочно_SMART_читается_не_чаще_5_минут()
    {
        var collector = Collector();
        collector.Collect(null, out _);
        _probe.Smart = DiskSmart.Warning;
        _clock.Advance(TimeSpan.FromMinutes(1));
        var cached = collector.Collect(null, out var urgent);
        Assert.False(urgent);
        Assert.Equal(DiskSmart.Ok, cached.Disks[0].Smart);
        Assert.Equal(1, _probe.SmartCalls);

        _clock.Advance(TimeSpan.FromMinutes(4));
        var h = collector.Collect(null, out urgent);
        Assert.True(urgent);
        Assert.Equal(DiskSmart.Warning, h.Disks[0].Smart);
        Assert.Equal(2, _probe.SmartCalls);
    }

    [Fact]
    public void Журнал_сбоев_читается_не_чаще_10_минут()
    {
        var collector = Collector();
        collector.Collect(null, out _);
        _clock.Advance(TimeSpan.FromMinutes(9));
        var h = collector.Collect(null, out _);
        Assert.Equal(1, _probe.CrashCalls);
        Assert.Equal(2u, h.UnexpectedShutdowns7D); // из кэша
        _clock.Advance(TimeSpan.FromMinutes(1));
        collector.Collect(null, out _);
        Assert.Equal(2, _probe.CrashCalls);
    }

    // ------------------------------------------------------------------ фоновый сбор

    private HealthMonitor Monitor(TimeSpan? timeout = null) =>
        new(Collector(), () => TimeSpan.FromSeconds(60), () => null, TimeSpan.FromSeconds(5), timeout ?? TimeSpan.FromSeconds(10), _clock);

    [Fact]
    public async Task Периферия_отключилась_срочно_температура_ждёт_интервала()
    {
        var monitor = Monitor();
        var urgent = 0;
        monitor.Urgent += () => Interlocked.Increment(ref urgent);

        await monitor.TickAsync(CancellationToken.None); // первый снимок — тоже повод отправить сразу
        Assert.Equal(1, urgent);
        Assert.Equal(650, monitor.Latest!.Health.CpuTempDecicelsius);

        _probe.CpuTemp = 900;
        _clock.Advance(TimeSpan.FromSeconds(5));
        await monitor.TickAsync(CancellationToken.None); // только периферия
        Assert.Equal(1, urgent);
        Assert.Equal(650, monitor.Latest!.Health.CpuTempDecicelsius);

        _probe.Unplug(PeripheralKind.Headset);
        _clock.Advance(TimeSpan.FromSeconds(5));
        await monitor.TickAsync(CancellationToken.None);
        Assert.Equal(2, urgent);
        Assert.False(Find(monitor.Latest!.Health, PeripheralKind.Headset).Connected);
        Assert.Equal(650, monitor.Latest!.Health.CpuTempDecicelsius); // снимок не пересобран

        _clock.Advance(TimeSpan.FromSeconds(50));
        await monitor.TickAsync(CancellationToken.None); // интервал — полный снимок, без срочности
        Assert.Equal(2, urgent);
        Assert.Equal(900, monitor.Latest!.Health.CpuTempDecicelsius);
        Assert.Equal(T0 + TimeSpan.FromSeconds(60), monitor.Latest!.CollectedAt);
    }

    [Fact]
    public async Task Зависший_источник_не_блокирует_и_сбор_не_копится()
    {
        var monitor = Monitor(TimeSpan.FromMilliseconds(200));
        await monitor.TickAsync(CancellationToken.None);
        var before = monitor.Latest;

        _probe.HangPeripherals = true;
        try
        {
            _clock.Advance(TimeSpan.FromSeconds(5));
            var sw = System.Diagnostics.Stopwatch.StartNew();
            await monitor.TickAsync(CancellationToken.None); // таймаут
            Assert.True(sw.Elapsed < TimeSpan.FromSeconds(5));
            var calls = _probe.PeripheralCalls;
            await monitor.TickAsync(CancellationToken.None); // прежний сбор ещё идёт — новый не начинается
            Assert.Equal(calls, _probe.PeripheralCalls);
            Assert.Same(before, monitor.Latest);
        }
        finally
        {
            _probe.HangPeripherals = false;
        }
    }

    // ------------------------------------------------------------------ когда health в Status

    [Fact]
    public void Health_в_Status_раз_в_интервал_и_сразу_при_срочном()
    {
        var gate = new HealthGate();
        var snap = new HealthSnapshot(new ComputerHealth { CpuTempDecicelsius = 500 }, T0);
        var every = TimeSpan.FromSeconds(60);
        var stale = TimeSpan.FromMinutes(3);

        Assert.Null(gate.Take(null, T0, every, stale)); // снимка ещё нет
        Assert.NotNull(gate.Take(snap, T0, every, stale)); // первый Status стрима
        Assert.Null(gate.Take(snap, T0.AddSeconds(2), every, stale)); // Status по смене состояния — без health
        gate.MarkUrgent();
        Assert.NotNull(gate.Take(snap, T0.AddSeconds(3), every, stale)); // срочно
        Assert.Null(gate.Take(snap, T0.AddSeconds(10), every, stale));
        Assert.NotNull(gate.Take(snap, T0.AddSeconds(63), every, stale)); // интервал
        gate.Reset();
        Assert.NotNull(gate.Take(snap, T0.AddSeconds(64), every, stale)); // новый стрим
        Assert.Null(gate.Take(snap, T0.AddMinutes(10), every, stale)); // снимок устарел (сбор завис)
    }

    [Fact]
    public async Task Runtime_кладёт_health_в_Status_по_правилу()
    {
        var platform = new FakePlatform();
        await using var rt = Runtimes.Create(_dir.Path, platform, _pki, time: _clock);
        Assert.Null(rt.NextStatus().Health); // снимка ещё нет — Status не ждёт сбора

        await rt.Health.TickAsync(CancellationToken.None);
        var first = rt.NextStatus();
        Assert.NotNull(first.Health);
        Assert.Equal(650, first.Health.CpuTempDecicelsius);
        Assert.Equal(1200u, first.Load.CpuBasisPoints);

        _clock.Advance(TimeSpan.FromSeconds(5));
        Assert.Null(rt.NextStatus().Health);

        platform.FakeHealth.Unplug(PeripheralKind.Mouse);
        await rt.Health.TickAsync(CancellationToken.None);
        var urgent = rt.NextStatus();
        Assert.NotNull(urgent.Health);
        Assert.False(urgent.Health.Peripherals.Single(p => p.Kind == PeripheralKind.Mouse).Connected);

        platform.FakeHealth.CpuTemp = 880;
        _clock.Advance(TimeSpan.FromSeconds(5));
        await rt.Health.TickAsync(CancellationToken.None);
        Assert.Null(rt.NextStatus().Health); // температура — ждём интервала
    }

    // ------------------------------------------------------------------ разбор данных Windows

    private static byte[] Edid(string? name)
    {
        var e = new byte[128];
        byte[] header = [0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00];
        header.CopyTo(e, 0);
        // «AUS»: A=1, U=21, S=19 → 0 00001 10101 10011
        var m = (1 << 10) | (21 << 5) | 19;
        e[8] = (byte)(m >> 8);
        e[9] = (byte)m;
        e[10] = 0x48;
        e[11] = 0x24;
        if (name is not null)
        {
            var d = 72;
            e[d + 3] = 0xFC;
            var bytes = System.Text.Encoding.ASCII.GetBytes(name + "\n");
            for (var i = 0; i < 13; i++) e[d + 5 + i] = i < bytes.Length ? bytes[i] : (byte)0x20;
        }

        return e;
    }

    [Fact]
    public void EDID_название_монитора_или_код_производителя()
    {
        Assert.Equal("VG248", HealthParsing.EdidName(Edid("VG248")));
        Assert.Equal("AUS 2448", HealthParsing.EdidName(Edid(null)));
        Assert.Null(HealthParsing.EdidName(new byte[128]));
        Assert.Null(HealthParsing.EdidName(Edid("X").AsSpan(0, 100)));
    }

    [Fact]
    public void Номера_дисков_тома_из_VOLUME_DISK_EXTENTS()
    {
        var buf = new byte[8 + 24 * 2];
        BitConverter.GetBytes(2u).CopyTo(buf, 0);
        BitConverter.GetBytes(0u).CopyTo(buf, 8);
        BitConverter.GetBytes(3u).CopyTo(buf, 8 + 24);
        Assert.Equal([0u, 3u], HealthParsing.VolumeDiskNumbers(buf));
        Assert.Empty(HealthParsing.VolumeDiskNumbers(new byte[4]));
        Assert.Equal([0u], HealthParsing.VolumeDiskNumbers(buf.AsSpan(0, 8 + 24))); // ответ короче заявленного
    }

    [Fact]
    public void SMART_из_HealthStatus_и_худший_диск_тома()
    {
        Assert.Equal(DiskSmart.Ok, HealthParsing.SmartFromHealthStatus(0));
        Assert.Equal(DiskSmart.Warning, HealthParsing.SmartFromHealthStatus(1));
        Assert.Equal(DiskSmart.Failing, HealthParsing.SmartFromHealthStatus(2));
        Assert.Equal(DiskSmart.Unspecified, HealthParsing.SmartFromHealthStatus(5));
        Assert.Equal(DiskSmart.Warning, HealthParsing.Worst([DiskSmart.Ok, DiskSmart.Unspecified, DiskSmart.Warning]));
        Assert.Equal(DiskSmart.Unspecified, HealthParsing.Worst([]));
    }

    [Fact]
    public void Гарнитура_любой_аудиовыход_кроме_монитора_SPDIF_и_сети()
    {
        Assert.True(HealthParsing.IsHeadsetFormFactor(3)); // Headphones
        Assert.True(HealthParsing.IsHeadsetFormFactor(5)); // Headset
        Assert.True(HealthParsing.IsHeadsetFormFactor(1)); // Speakers (USB-гарнитура часто так себя называет)
        Assert.False(HealthParsing.IsHeadsetFormFactor(9)); // HDMI/DisplayPort монитора
        Assert.False(HealthParsing.IsHeadsetFormFactor(8)); // S/PDIF
        Assert.False(HealthParsing.IsHeadsetFormFactor(0)); // удалённое
    }

    [Fact]
    public void Запрос_журнала_и_подсчёт_внезапных_выключений()
    {
        Assert.Equal("*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and EventID=41 and TimeCreated[timediff(@SystemTime) <= 604800000]]]",
            HealthParsing.EventQuery("Microsoft-Windows-Kernel-Power", 41, TimeSpan.FromDays(7)));
        Assert.Throws<ArgumentException>(() => HealthParsing.EventQuery("x' or '1", 1, TimeSpan.FromDays(1)));
        Assert.Equal(3u, HealthParsing.UnexpectedShutdowns(3, 2)); // одна авария — и 41, и 6008
        Assert.Equal(1000u, HealthParsing.ToMbps(1_000_000_000));
        Assert.Equal(0u, HealthParsing.ToMbps(-1));
    }

    [Fact]
    public void Linux_источник_место_и_аптайм_без_исключений()
    {
        var h = new HealthCollector(new LinuxHealthProbe()).Collect("127.0.0.1", out _);
        Assert.True(h.UptimeSeconds > 0);
        Assert.Equal(0, h.CpuTempDecicelsius);
        Assert.Empty(h.Peripherals);
    }

    /// <summary>Часы, которые двигает тест (таймеры — системные).</summary>
    private sealed class ManualClock(DateTimeOffset now) : TimeProvider
    {
        private long _ticks = now.UtcTicks;

        public override DateTimeOffset GetUtcNow() => new(Interlocked.Read(ref _ticks), TimeSpan.Zero);

        public void Advance(TimeSpan by) => Interlocked.Add(ref _ticks, by.Ticks);
    }
}
