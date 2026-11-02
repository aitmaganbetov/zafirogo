using System.Threading.Channels;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;

namespace Zafiro.Agent.Service.Ipc;

/// <summary>Подключение UI, прошедшее проверку (SID/сеанс/exe на Windows, uid на Linux).</summary>
public sealed record IpcClient(System.IO.Stream Stream, string Identity) : IAsyncDisposable
{
    public ValueTask DisposeAsync() => Stream.DisposeAsync();
}

/// <summary>Локальный слушатель IPC (именованный канал Windows / unix socket Linux).</summary>
public interface IIpcListener : IAsyncDisposable
{
    /// <summary>Следующий проверенный клиент; чужие подключения закрываются внутри.</summary>
    Task<IpcClient> AcceptAsync(CancellationToken cancellationToken);
}

/// <summary>
/// Сервер IPC службы для UI: запросы (<see cref="IpcDispatcher"/>) и события (снимок состояния, сообщения сотрудника,
/// чат). Новому клиенту сразу уходит снимок. Не больше <see cref="MaxClients"/> клиентов и
/// <see cref="MaxInflight"/> запросов на клиента одновременно.
/// </summary>
public sealed class IpcServer : BackgroundService
{
    public const int MaxClients = 4;
    public const int MaxInflight = 8;

    /// <summary>Сколько уведомлений держать, пока UI не подключён.</summary>
    public const int MaxPendingNotices = 16;

    /// <summary>Сколько держать уведомление, пока UI не подключён.</summary>
    public static readonly TimeSpan NoticeLifetime = TimeSpan.FromMinutes(5);

    private readonly IIpcListener _listener;
    private readonly IpcDispatcher _dispatcher;
    private readonly AgentRuntime _runtime;
    private readonly TimeProvider _time;
    private readonly ILogger _log;
    private readonly object _noticeGate = new();
    private readonly Queue<(DateTimeOffset At, Event Event)> _pendingNotices = new();
    private int _clients;

    public IpcServer(IIpcListener listener, IpcDispatcher dispatcher, AgentRuntime runtime, ILogger<IpcServer> log, TimeProvider? time = null)
    {
        _listener = listener;
        _dispatcher = dispatcher;
        _runtime = runtime;
        _log = log;
        _time = time ?? TimeProvider.System;
    }

    /// <summary>Сколько UI подключено (координатор экрана не запускает второй UI).</summary>
    public int ConnectedClients => Volatile.Read(ref _clients);

    /// <summary>Событие службы (не из <see cref="AgentRuntime"/>) для подключённых UI.</summary>
    private event Action<Event>? Published;

    /// <summary>Есть уведомления, ради которых UI нужно запустить (UI не подключён).</summary>
    public bool HasPendingNotices
    {
        get
        {
            lock (_noticeGate)
            {
                DropExpiredNotices();
                return _pendingNotices.Count > 0;
            }
        }
    }

    /// <summary>
    /// Уведомление для UI: подключённым — сразу, иначе — в очередь до подключения (координатор запустит UI).
    /// </summary>
    public void Publish(Event e)
    {
        ArgumentNullException.ThrowIfNull(e);
        if (ConnectedClients > 0 && Published is { } published)
        {
            published(e);
            return;
        }

        if (IsNotice(e)) Enqueue(e);
    }

    /// <summary>Уведомления, ради которых UI запускается во время сессии (UI тогда не работает).</summary>
    public static bool IsNotice(Event e) => e?.KindCase switch
    {
        Event.KindOneofCase.ShowMessage or Event.KindOneofCase.SessionEnding => true,
        Event.KindOneofCase.ChatMessage => e.ChatMessage.Message?.FromStaff == true,
        _ => false,
    };

    private void Enqueue(Event e)
    {
        lock (_noticeGate)
        {
            DropExpiredNotices();
            _pendingNotices.Enqueue((_time.GetUtcNow(), e.Clone()));
            while (_pendingNotices.Count > MaxPendingNotices) _pendingNotices.Dequeue();
        }
    }

    private List<Event> TakeNotices()
    {
        lock (_noticeGate)
        {
            DropExpiredNotices();
            var list = _pendingNotices.Select(n => n.Event).ToList();
            _pendingNotices.Clear();
            return list;
        }
    }

    private void DropExpiredNotices()
    {
        var now = _time.GetUtcNow();
        while (_pendingNotices.Count > 0 && now - _pendingNotices.Peek().At > NoticeLifetime) _pendingNotices.Dequeue();
    }

    // Событие рантайма, пока ни один UI не подключён: уведомление ждёт UI в очереди.
    private void OnRuntimeEvent(Event e)
    {
        if (ConnectedClients == 0 && IsNotice(e)) Enqueue(e);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        _runtime.UiEvent += OnRuntimeEvent;
        try
        {
            await AcceptLoopAsync(stoppingToken).ConfigureAwait(false);
        }
        finally
        {
            _runtime.UiEvent -= OnRuntimeEvent;
        }
    }

    private async Task AcceptLoopAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            IpcClient client;
            try
            {
                client = await _listener.AcceptAsync(stoppingToken).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or System.Net.Sockets.SocketException)
            {
                _log.LogWarning("ipc accept failed: {Error}", ex.Message);
                await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken).ConfigureAwait(false);
                continue;
            }

            if (Interlocked.Increment(ref _clients) > MaxClients)
            {
                Interlocked.Decrement(ref _clients);
                await client.DisposeAsync().ConfigureAwait(false);
                continue;
            }

            _ = Task.Run(() => ServeAsync(client, stoppingToken), CancellationToken.None);
        }

        await _listener.DisposeAsync().ConfigureAwait(false);
    }

    private async Task ServeAsync(IpcClient client, CancellationToken stoppingToken)
    {
        using var cts = CancellationTokenSource.CreateLinkedTokenSource(stoppingToken);
        var outbox = Channel.CreateBounded<Envelope>(new BoundedChannelOptions(256) { FullMode = BoundedChannelFullMode.DropOldest, SingleReader = true });
        void OnUi(Event e) => outbox.Writer.TryWrite(new Envelope { Event = e });
        _runtime.UiEvent += OnUi;
        Published += OnUi;
        _log.LogInformation("ui connected: {Client}", client.Identity);
        // Не освобождается явно: запросы могут завершиться после закрытия соединения.
        var inflight = new SemaphoreSlim(MaxInflight, MaxInflight);
        var writer = Task.Run(async () =>
        {
            await foreach (var env in outbox.Reader.ReadAllAsync(cts.Token).ConfigureAwait(false))
                await IpcFraming.WriteAsync(client.Stream, env, cts.Token).ConfigureAwait(false);
        }, CancellationToken.None);
        try
        {
            outbox.Writer.TryWrite(new Envelope { Event = new Event { Snapshot = _runtime.State.BuildSnapshot() } });
            foreach (var notice in TakeNotices()) outbox.Writer.TryWrite(new Envelope { Event = notice });
            while (!cts.IsCancellationRequested)
            {
                var env = await IpcFraming.ReadAsync(client.Stream, cts.Token).ConfigureAwait(false);
                if (env is null) break;
                if (env.KindCase != Envelope.KindOneofCase.Request) continue;
                await inflight.WaitAsync(cts.Token).ConfigureAwait(false);
                var request = env.Request;
                _ = Task.Run(async () =>
                {
                    try
                    {
                        var response = await _dispatcher.DispatchAsync(request, cts.Token).ConfigureAwait(false);
                        outbox.Writer.TryWrite(new Envelope { Response = response });
                    }
                    finally
                    {
                        inflight.Release();
                    }
                }, CancellationToken.None);
            }
        }
        catch (Exception ex) when (ex is IOException or InvalidDataException or EndOfStreamException or OperationCanceledException or ObjectDisposedException)
        {
            _log.LogDebug("ui connection closed: {Error}", ex.Message);
        }
        finally
        {
            _runtime.UiEvent -= OnUi;
            Published -= OnUi;
            outbox.Writer.TryComplete();
            await cts.CancelAsync().ConfigureAwait(false);
            try
            {
                await writer.ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is IOException or OperationCanceledException or ObjectDisposedException)
            {
            }

            await client.DisposeAsync().ConfigureAwait(false);
            Interlocked.Decrement(ref _clients);
            _log.LogInformation("ui disconnected: {Client}", client.Identity);
        }
    }
}
