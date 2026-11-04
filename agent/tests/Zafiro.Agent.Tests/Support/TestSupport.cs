using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Core.Transport;
using Zafiro.Agent.Core.Updates;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests.Support;

/// <summary>Временный каталог теста.</summary>
public sealed class TempDir : IDisposable
{
    public TempDir() => Path = Directory.CreateTempSubdirectory("zafiro-agent-test-").FullName;

    public string Path { get; }

    public void Dispose()
    {
        try
        {
            Directory.Delete(Path, recursive: true);
        }
        catch (IOException)
        {
        }
    }
}

/// <summary>Платформа-заглушка: считает действия, ничего не делает.</summary>
public sealed class FakePlatform : IPlatform
{
    public FakePlatform(string? mac = null)
    {
        Mac = mac ?? RandomMac();
        UpdateDirectory = System.IO.Path.Combine(System.IO.Path.GetTempPath(), "zafiro-agent-test-update-" + Guid.NewGuid().ToString("N"));
    }

    public string Mac { get; }

    public int Reboots;
    public int Shutdowns;
    public int Installs;
    public List<string> Woken { get; } = [];

    public AgentCapabilities Capabilities { get; } = new() { SelfUpdate = true, WakeOnLan = true, PowerControl = true, ShowMessage = true, ServiceMode = true };

    public string UpdateDirectory { get; }

    /// <summary>Здоровье ПК: управляемый тестом источник.</summary>
    public FakeHealthProbe FakeHealth { get; } = new();

    public IHealthProbe Health => FakeHealth;

    public static string RandomMac()
    {
        var b = RandomNumberGenerator.GetBytes(6);
        b[0] = (byte)((b[0] & 0xFC) | 0x02); // локально администрируемый, unicast
        return string.Join(':', b.Select(x => x.ToString("X2", System.Globalization.CultureInfo.InvariantCulture)));
    }

    public DeviceInfo GetDeviceInfo(string agentVersion, bool diskless) => new()
    {
        HardwareUuid = "TEST-" + Mac.Replace(":", "", StringComparison.Ordinal),
        MacAddress = Mac,
        Hostname = "agent-test",
        AgentVersion = agentVersion,
        OsVersion = "Linux test",
        CpuModel = "Test CPU",
        CpuCores = 4,
        RamTotalMb = 8192,
        GpuModel = "Test GPU",
        LocalIp = "10.0.0.5",
        Diskless = diskless,
    };

    public string GetBootId() => "00000000-0000-4000-8000-000000000001";

    public ComputerLoad GetLoad() => new() { CpuBasisPoints = 1200, RamBasisPoints = 4500, AgentMemoryMb = 30 };

    public Task<bool> RebootAsync(TimeSpan delay, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref Reboots);
        return Task.FromResult(true);
    }

    public Task<bool> ShutdownAsync(TimeSpan delay, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref Shutdowns);
        return Task.FromResult(true);
    }

    public Task<int> WakeOnLanAsync(IReadOnlyCollection<string> macAddresses, CancellationToken cancellationToken)
    {
        lock (Woken) Woken.AddRange(macAddresses);
        return Task.FromResult(macAddresses.Count);
    }

    public Task<bool> InstallUpdateAsync(string installerPath, CancellationToken cancellationToken)
    {
        Interlocked.Increment(ref Installs);
        return Task.FromResult(true);
    }
}

/// <summary>Источник здоровья для тестов: значения задаёт тест, любой источник можно «сломать».</summary>
public sealed class FakeHealthProbe : IHealthProbe
{
    private readonly object _gate = new();
    private readonly List<PeripheralDevice> _present =
    [
        new(PeripheralKind.Mouse, "Logitech G102"),
        new(PeripheralKind.Keyboard, "HyperX Alloy"),
        new(PeripheralKind.Headset, "Наушники (HyperX Cloud II)"),
        new(PeripheralKind.Monitor, "VG248"),
    ];

    public int CpuTemp = 650;
    public int SmartCalls;
    public int DiskCalls;
    public int CrashCalls;
    public int PeripheralCalls;
    public DiskSmart Smart = DiskSmart.Ok;
    public Exception? CpuFails;
    public Exception? GpuFails;
    public Exception? PeripheralsFail;
    public bool HangPeripherals;

    public void Unplug(PeripheralKind kind)
    {
        lock (_gate) _present.RemoveAll(p => p.Kind == kind);
    }

    public void Plug(PeripheralKind kind, string name)
    {
        lock (_gate) _present.Add(new PeripheralDevice(kind, name));
    }

    public int CpuTemperatureDeciCelsius() => CpuFails is { } e ? throw e : Volatile.Read(ref CpuTemp);

    public GpuReading? Gpu() => GpuFails is { } e ? throw e : new GpuReading(710, 4200, 3100, true);

    public IReadOnlyList<DiskSpace> Disks()
    {
        Interlocked.Increment(ref DiskCalls);
        return [new DiskSpace("C:", true, 476_000, 120_000), new DiskSpace("D:", false, 953_000, 800_000)];
    }

    public IReadOnlyDictionary<string, DiskSmart> SmartStatus(IReadOnlyList<DiskSpace> disks)
    {
        Interlocked.Increment(ref SmartCalls);
        return new Dictionary<string, DiskSmart> { ["C:"] = Smart, ["D:"] = DiskSmart.Ok };
    }

    public PeripheralScan Peripherals()
    {
        Interlocked.Increment(ref PeripheralCalls);
        if (PeripheralsFail is { } e) throw e;
        while (Volatile.Read(ref HangPeripherals)) Thread.Sleep(10);
        lock (_gate) return new PeripheralScan([PeripheralKind.Mouse, PeripheralKind.Keyboard, PeripheralKind.Headset, PeripheralKind.Monitor], _present.ToList());
    }

    public uint LinkSpeedMbps(string? serverHost) => 1000;

    public ulong UptimeSeconds() => 3600;

    public CrashCounts? Crashes(DateTimeOffset since)
    {
        Interlocked.Increment(ref CrashCalls);
        return new CrashCounts(2, 1);
    }
}

/// <summary>Тестовый PKI: корень → выпускающий CA → сертификаты ПК (как dev-PKI сервера, ECDSA P-256).</summary>
public sealed class TestPki : IDisposable
{
    private static readonly Oid ClientAuth = new("1.3.6.1.5.5.7.3.2");

    public TestPki(string name = "Test Zafiro Root")
    {
        RootKey = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        var rootReq = new CertificateRequest("CN=" + name, RootKey, HashAlgorithmName.SHA256);
        rootReq.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, false, 0, true));
        rootReq.CertificateExtensions.Add(new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        Root = rootReq.CreateSelfSigned(DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddYears(5));

        IssuingKey = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        var caReq = new CertificateRequest("CN=" + name + " Issuing", IssuingKey, HashAlgorithmName.SHA256);
        caReq.CertificateExtensions.Add(new X509BasicConstraintsExtension(true, true, 0, true));
        caReq.CertificateExtensions.Add(new X509KeyUsageExtension(X509KeyUsageFlags.KeyCertSign | X509KeyUsageFlags.CrlSign, true));
        using var caPub = caReq.Create(Root, DateTimeOffset.UtcNow.AddDays(-1), DateTimeOffset.UtcNow.AddYears(2), RandomNumberGenerator.GetBytes(16));
        Issuing = caPub.CopyWithPrivateKey(IssuingKey);
    }

    public ECDsa RootKey { get; }

    public X509Certificate2 Root { get; }

    public ECDsa IssuingKey { get; }

    public X509Certificate2 Issuing { get; }

    public string RootPem => Root.ExportCertificatePem();

    public string IssuingPem => Issuing.ExportCertificatePem();

    /// <summary>Сертификат ПК на открытый ключ <paramref name="key"/> (как pki.CA.Issue сервера).</summary>
    public string IssueClientPem(ECDsa key, long club = 1, long computer = 7, int days = 30)
    {
        var req = new CertificateRequest($"CN=computer:{computer}, O=club:{club}", key, HashAlgorithmName.SHA256);
        req.CertificateExtensions.Add(new X509BasicConstraintsExtension(false, false, 0, true));
        req.CertificateExtensions.Add(new X509KeyUsageExtension(X509KeyUsageFlags.DigitalSignature, true));
        req.CertificateExtensions.Add(new X509EnhancedKeyUsageExtension([ClientAuth], false));
        var san = new SubjectAlternativeNameBuilder();
        san.AddUri(new Uri($"zafiro://club/{club}/computer/{computer}"));
        req.CertificateExtensions.Add(san.Build());
        using var cert = req.Create(Issuing, DateTimeOffset.UtcNow.AddMinutes(-5), DateTimeOffset.UtcNow.AddDays(days), RandomNumberGenerator.GetBytes(16));
        return cert.ExportCertificatePem();
    }

    public TrustAnchors Anchors() => TrustAnchors.FromPem(RootPem);

    public void Dispose()
    {
        Root.Dispose();
        Issuing.Dispose();
        RootKey.Dispose();
        IssuingKey.Dispose();
    }
}

/// <summary>Сборка AgentRuntime для unit-тестов (без сервера).</summary>
public static class Runtimes
{
    public static AgentRuntime Create(string dataDir, IPlatform platform, TestPki pki, IReadOnlyDictionary<string, byte[]>? updateKeys = null,
        UpdateInstaller? installer = null, TimeProvider? time = null, AgentOptions? options = null)
    {
        options ??= new AgentOptions { EnrollEndpoint = "localhost:1", DataDirectory = dataDir };
        options.DataDirectory = dataDir;
        var store = new FileCredentialStore(Path.Combine(dataDir, "creds"));
        return new AgentRuntime(options, store, platform, pki.Anchors(), new UpdateVerifier(updateKeys ?? new Dictionary<string, byte[]>()),
            installer, NullLoggerFactory.Instance, time);
    }
}
