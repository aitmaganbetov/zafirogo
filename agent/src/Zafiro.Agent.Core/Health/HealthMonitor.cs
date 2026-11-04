using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Health;

/// <summary>Последний снимок здоровья и когда он собран (полный сбор; опрос периферии время не меняет).</summary>
public sealed record HealthSnapshot(ComputerHealth Health, DateTimeOffset CollectedAt);

/// <summary>
/// Фоновый сбор здоровья ПК (Status.health). Раз в <see cref="HealthMonitor"/>-интервал — полный снимок, между ними
/// каждые <c>pollInterval</c> — только периферия. Поток Status сбор не ждёт: берёт <see cref="Latest"/>. Источники
/// вызываются в отдельной задаче с таймаутом; зависший источник не блокирует ни Status, ни следующий опрос
/// (новый сбор не начинается, пока не закончится прежний, — потоки не копятся), снимок стареет и перестаёт
/// отправляться (<see cref="HealthGate"/>). <see cref="Urgent"/> — изменилась периферия или SMART: Status сразу.
/// </summary>
public sealed class HealthMonitor
{
    private readonly HealthCollector _collector;
    private readonly Func<TimeSpan> _collectInterval;
    private readonly Func<string?> _serverHost;
    private readonly TimeProvider _time;
    private readonly ILogger _log;
    private readonly TimeSpan _pollInterval;
    private readonly TimeSpan _sourceTimeout;
    private Task<bool>? _inflight;
    private DateTimeOffset? _lastFull;
    private HealthSnapshot? _latest;
    private bool _hangLogged;

    public HealthMonitor(HealthCollector collector, Func<TimeSpan> collectInterval, Func<string?> serverHost, TimeSpan pollInterval,
        TimeSpan sourceTimeout, TimeProvider? time = null, ILogger? log = null)
    {
        _collector = collector ?? throw new ArgumentNullException(nameof(collector));
        _collectInterval = collectInterval ?? throw new ArgumentNullException(nameof(collectInterval));
        _serverHost = serverHost ?? throw new ArgumentNullException(nameof(serverHost));
        _pollInterval = pollInterval > TimeSpan.Zero ? pollInterval : TimeSpan.FromSeconds(5);
        _sourceTimeout = sourceTimeout > TimeSpan.Zero ? sourceTimeout : TimeSpan.FromSeconds(20);
        _time = time ?? TimeProvider.System;
        _log = log ?? NullLogger.Instance;
    }

    /// <summary>Последний снимок; null — ещё не собран.</summary>
    public HealthSnapshot? Latest => Volatile.Read(ref _latest);

    /// <summary>Срочное изменение (периферия, SMART) или первый снимок. Вызывается из фоновой задачи.</summary>
    public event Action? Urgent;

    /// <summary>Сбор, пока не отменён <paramref name="cancellationToken"/>.</summary>
    public async Task RunAsync(CancellationToken cancellationToken)
    {
        using var timer = new PeriodicTimer(_pollInterval, _time);
        do
        {
            try
            {
                await TickAsync(cancellationToken).ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                _log.LogWarning("health collection failed: {Error}", ex.Message);
            }
        }
        while (await timer.WaitForNextTickAsync(cancellationToken).ConfigureAwait(false));
    }

    /// <summary>Один шаг: полный снимок, если подошёл интервал, иначе — опрос периферии.</summary>
    internal async Task TickAsync(CancellationToken cancellationToken)
    {
        if (_inflight is { IsCompleted: false })
        {
            if (!_hangLogged) _log.LogWarning("health collection is still running after {Timeout}: a source hangs, health is not sent", _sourceTimeout);
            _hangLogged = true;
            return;
        }

        _hangLogged = false;
        var now = _time.GetUtcNow();
        var full = _latest is null || _lastFull is null || now - _lastFull.Value >= _collectInterval();
        var host = full ? _serverHost() : null;
        var work = Task.Run(() => full ? CollectFull(host, now) : PollPeripherals(), CancellationToken.None);
        _inflight = work;
        bool urgent;
        try
        {
            urgent = await work.WaitAsync(_sourceTimeout, _time, cancellationToken).ConfigureAwait(false);
        }
        catch (TimeoutException)
        {
            _log.LogWarning("health collection did not finish in {Timeout}", _sourceTimeout);
            return;
        }

        if (urgent) Urgent?.Invoke();
    }

    private bool CollectFull(string? host, DateTimeOffset startedAt)
    {
        var first = _latest is null;
        var health = _collector.Collect(host, out var urgent);
        _lastFull = startedAt;
        Volatile.Write(ref _latest, new HealthSnapshot(health, _time.GetUtcNow()));
        return urgent || first;
    }

    private bool PollPeripherals()
    {
        if (!_collector.RefreshPeripherals()) return false;
        var latest = _latest;
        if (latest is null) return false;
        var health = latest.Health.Clone();
        health.Peripherals.Clear();
        health.Peripherals.AddRange(_collector.CurrentPeripherals);
        Volatile.Write(ref _latest, latest with { Health = health });
        return true;
    }
}

/// <summary>
/// Когда класть health в Status (agent.proto, Status.health): раз в интервал статуса и сразу после срочного изменения;
/// после переподключения — в первый же Status. Устаревший снимок (сбор завис) не отправляется: сервер держит последний
/// присланный. Потокобезопасен.
/// </summary>
public sealed class HealthGate
{
    // Тик статуса приходит ровно через интервал после прошлого Status — небольшой запас против дрожания таймера.
    private static readonly TimeSpan Slack = TimeSpan.FromSeconds(1);
    private readonly object _gate = new();
    private DateTimeOffset? _lastSent;
    private bool _urgent;

    /// <summary>Новый стрим: следующий Status — с health.</summary>
    public void Reset()
    {
        lock (_gate) _lastSent = null;
    }

    /// <summary>Срочное изменение: следующий Status — с health, не дожидаясь интервала.</summary>
    public void MarkUrgent()
    {
        lock (_gate) _urgent = true;
    }

    /// <summary>health для этого Status или null. <paramref name="staleAfter"/> — старше этого снимок не шлётся.</summary>
    public ComputerHealth? Take(HealthSnapshot? latest, DateTimeOffset now, TimeSpan every, TimeSpan staleAfter)
    {
        if (latest is null || now - latest.CollectedAt > staleAfter) return null;
        lock (_gate)
        {
            if (!_urgent && _lastSent is { } last && now - last < every - Slack) return null;
            _urgent = false;
            _lastSent = now;
            return latest.Health;
        }
    }
}
