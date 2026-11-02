using System.Threading.Channels;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Runtime;
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

    private readonly IIpcListener _listener;
    private readonly IpcDispatcher _dispatcher;
    private readonly AgentRuntime _runtime;
    private readonly ILogger _log;
    private int _clients;

    public IpcServer(IIpcListener listener, IpcDispatcher dispatcher, AgentRuntime runtime, ILogger<IpcServer> log)
    {
        _listener = listener;
        _dispatcher = dispatcher;
        _runtime = runtime;
        _log = log;
    }

    /// <summary>Сколько UI подключено (координатор экрана не запускает второй UI).</summary>
    public int ConnectedClients => Volatile.Read(ref _clients);

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
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
