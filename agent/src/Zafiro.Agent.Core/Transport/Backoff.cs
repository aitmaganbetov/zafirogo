namespace Zafiro.Agent.Core.Transport;

/// <summary>
/// Задержки переподключения (agent.proto, шапка п. 8; docs/agent-protocol.md §2): экспонента с full jitter
/// <c>delay = random(0, min(60 с, 1 с · 2^attempt))</c>; сервер может потребовать не раньше RetryInfo;
/// Drain — случайный момент в окне reconnect_within, чтобы 50 000 ПК не пришли разом.
/// </summary>
public static class Backoff
{
    /// <summary>Потолок задержки.</summary>
    public static readonly TimeSpan Max = TimeSpan.FromSeconds(60);

    /// <summary>Стрим, проживший столько, сбрасывает счётчик попыток.</summary>
    public static readonly TimeSpan StableAfter = TimeSpan.FromSeconds(60);

    /// <summary>Пауза после PROTOCOL_VERSION_UNSUPPORTED (не чаще раза в 10 минут).</summary>
    public static readonly TimeSpan ProtocolUnsupported = TimeSpan.FromMinutes(10);

    /// <summary>Верхняя граница окна попытки <paramref name="attempt"/> (0, 1, 2 …): min(60 с, 2^attempt с).</summary>
    public static TimeSpan Ceiling(int attempt)
    {
        if (attempt < 0) attempt = 0;
        if (attempt >= 6) return Max; // 2^6 = 64 с > 60 с
        return TimeSpan.FromSeconds(Math.Min(Max.TotalSeconds, 1 << attempt));
    }

    /// <summary>Full jitter: равномерно в [0, Ceiling(attempt)); не меньше <paramref name="retryAfter"/>, если сервер его задал.</summary>
    public static TimeSpan Next(int attempt, Random random, TimeSpan? retryAfter = null)
    {
        ArgumentNullException.ThrowIfNull(random);
        var ceiling = Ceiling(attempt);
        var delay = TimeSpan.FromTicks((long)(random.NextDouble() * ceiling.Ticks));
        if (retryAfter is { } ra && ra > delay) delay = ra;
        return delay;
    }

    /// <summary>Drain: случайно в [0, within); пустое или неположительное окно — 30 с (agent.proto п. 8).</summary>
    public static TimeSpan Drain(TimeSpan? within, Random random)
    {
        ArgumentNullException.ThrowIfNull(random);
        var w = within is { } x && x > TimeSpan.Zero ? x : TimeSpan.FromSeconds(30);
        return TimeSpan.FromTicks((long)(random.NextDouble() * w.Ticks));
    }

    /// <summary>Jitter продления сертификата: renew_after + random(0, 24 ч) (docs/agent-protocol.md §12.6), но не позже 90 % оставшегося срока.</summary>
    public static DateTimeOffset RenewAt(DateTimeOffset renewAfter, DateTimeOffset notAfter, Random random)
    {
        ArgumentNullException.ThrowIfNull(random);
        var window = TimeSpan.FromHours(24);
        var left = notAfter - renewAfter;
        if (left <= TimeSpan.Zero) return renewAfter;
        if (window > left * 0.9) window = left * 0.9;
        return renewAfter + TimeSpan.FromTicks((long)(random.NextDouble() * window.Ticks));
    }
}
