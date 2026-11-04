using System.Buffers.Binary;
using System.Globalization;
using System.Text;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Разбор данных источников здоровья Windows (без вызовов ОС — проверяется тестами на любой платформе):
/// EDID монитора, ответ IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS, MSFT_PhysicalDisk.HealthStatus, форм-фактор аудиовыхода,
/// XPath журнала System.
/// </summary>
public static class HealthParsing
{
    private static readonly byte[] EdidHeader = [0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00];

    /// <summary>
    /// Название монитора из EDID: дескриптор 0xFC («Monitor name»), иначе код производителя PNP и код модели («AOC 2460»).
    /// null — не EDID.
    /// </summary>
    public static string? EdidName(ReadOnlySpan<byte> edid)
    {
        if (edid.Length < 128 || !edid[..8].SequenceEqual(EdidHeader)) return null;
        for (var off = 54; off + 18 <= 126; off += 18)
        {
            var d = edid.Slice(off, 18);
            if (d[0] != 0 || d[1] != 0 || d[2] != 0 || d[3] != 0xFC) continue;
            var text = d[5..];
            var end = text.IndexOf((byte)0x0A);
            if (end >= 0) text = text[..end];
            var sb = new StringBuilder(text.Length);
            foreach (var b in text) sb.Append(b is >= 0x20 and < 0x7F ? (char)b : ' ');
            var name = sb.ToString().Trim();
            if (name.Length > 0) return name;
        }

        // Производитель: три буквы по 5 бит (big-endian), модель — little-endian.
        var m = BinaryPrimitives.ReadUInt16BigEndian(edid[8..10]);
        char L(int v) => v is >= 1 and <= 26 ? (char)('A' + v - 1) : '?';
        var vendor = new string([L((m >> 10) & 0x1F), L((m >> 5) & 0x1F), L(m & 0x1F)]);
        var product = BinaryPrimitives.ReadUInt16LittleEndian(edid[10..12]);
        return vendor + " " + product.ToString("X4", CultureInfo.InvariantCulture);
    }

    /// <summary>Номера физических дисков тома из VOLUME_DISK_EXTENTS (DWORD count, выравнивание, DISK_EXTENT по 24 байта).</summary>
    public static IReadOnlyList<uint> VolumeDiskNumbers(ReadOnlySpan<byte> extents)
    {
        if (extents.Length < 8) return [];
        var count = BinaryPrimitives.ReadUInt32LittleEndian(extents);
        var list = new List<uint>();
        for (var i = 0; i < count && 8 + (i + 1) * 24 <= extents.Length; i++)
        {
            var n = BinaryPrimitives.ReadUInt32LittleEndian(extents.Slice(8 + i * 24, 4));
            if (!list.Contains(n)) list.Add(n);
        }

        return list;
    }

    /// <summary>MSFT_PhysicalDisk.HealthStatus: 0 Healthy, 1 Warning, 2 Unhealthy, 5 Unknown.</summary>
    public static DiskSmart SmartFromHealthStatus(long healthStatus) => healthStatus switch
    {
        0 => DiskSmart.Ok,
        1 => DiskSmart.Warning,
        2 => DiskSmart.Failing,
        _ => DiskSmart.Unspecified,
    };

    /// <summary>SMART тома на нескольких дисках — худший из измеренных.</summary>
    public static DiskSmart Worst(IEnumerable<DiskSmart> states)
    {
        ArgumentNullException.ThrowIfNull(states);
        var worst = DiskSmart.Unspecified;
        foreach (var s in states)
        {
            if (Rank(s) > Rank(worst)) worst = s;
        }

        return worst;

        static int Rank(DiskSmart s) => s switch
        {
            DiskSmart.Failing => 3,
            DiskSmart.Warning => 2,
            DiskSmart.Ok => 1,
            _ => 0,
        };
    }

    /// <summary>
    /// Аудиовыход считается гарнитурой (agent.proto: «аудиоустройство вывода, кроме встроенного»), если это не выход на
    /// монитор (HDMI/DisplayPort), не S/PDIF и не сетевое/цифровое транзитное устройство. EndpointFormFactor:
    /// 0 RemoteNetworkDevice, 1 Speakers, 2 LineLevel, 3 Headphones, 4 Microphone, 5 Headset, 6 Handset,
    /// 7 UnknownDigitalPassthrough, 8 SPDIF, 9 DigitalAudioDisplayDevice, 10 UnknownFormFactor.
    /// </summary>
    public static bool IsHeadsetFormFactor(uint formFactor) => formFactor is not (0 or 4 or 7 or 8 or 9);

    /// <summary>Гарнитура/наушники по форм-фактору — их название показывается первым.</summary>
    public static bool IsHeadphonesFormFactor(uint formFactor) => formFactor is 3 or 5 or 6;

    /// <summary>XPath журнала System: события провайдера с кодом за последние <paramref name="window"/>.</summary>
    public static string EventQuery(string provider, int eventId, TimeSpan window)
    {
        ArgumentException.ThrowIfNullOrEmpty(provider);
        if (provider.Contains('\'', StringComparison.Ordinal)) throw new ArgumentException("provider must not contain quotes", nameof(provider));
        var ms = (long)Math.Max(0, window.TotalMilliseconds);
        return string.Create(CultureInfo.InvariantCulture,
            $"*[System[Provider[@Name='{provider}'] and EventID={eventId} and TimeCreated[timediff(@SystemTime) <= {ms}]]]");
    }

    /// <summary>
    /// Внезапные выключения: одна авария обычно пишет и Kernel-Power 41 (при следующей загрузке), и EventLog 6008 —
    /// берётся большее из двух чисел, а не сумма (иначе одна авария считалась бы дважды).
    /// </summary>
    public static uint UnexpectedShutdowns(uint kernelPower41, uint eventLog6008) => Math.Max(kernelPower41, eventLog6008);

    /// <summary>Скорость интерфейса (бит/с, -1 — неизвестно) в Мбит/с; 0 — не измерено.</summary>
    public static uint ToMbps(long bitsPerSecond) => bitsPerSecond <= 0 ? 0 : (uint)Math.Min(uint.MaxValue, bitsPerSecond / 1_000_000);
}
