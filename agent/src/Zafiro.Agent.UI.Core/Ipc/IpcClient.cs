using System.Collections.Concurrent;
using System.IO.Pipes;
using System.Net.Sockets;
using System.Runtime.Versioning;
using System.Security.Principal;
using System.Threading.Channels;
using Google.Protobuf;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;

namespace Zafiro.Agent.UI.Ipc;

/// <summary>Вызовы службы и её события (для моста и тестов).</summary>
public interface IIpcTransport
{
    /// <summary>Подключён ли канал к службе сейчас.</summary>
    bool IsConnected { get; }

    /// <summary>Запрос к службе; при обрыве канала — <see cref="IOException"/>, без ответа за timeout — <see cref="TimeoutException"/>.</summary>
    Task<Response> CallAsync(string method, ByteString payload, CancellationToken cancellationToken, TimeSpan? timeout = null);

    /// <summary>Событие службы (поток чтения канала).</summary>
    event Action<Event>? EventReceived;

    /// <summary>Канал подключён (true) или оборван (false).</summary>
    event Action<bool>? ConnectionChanged;
}

/// <summary>
/// Клиент IPC службы агента (ipc.proto): кадры <see cref="IpcFraming"/>, запросы с id и ожиданием ответа, события.
/// Канал оборвался (служба перезапускается при обновлении) — переподключение с паузой 0,5 … 5 с; ожидающие запросы
/// завершаются <see cref="IOException"/>. Секретов у UI нет: ключ ПК и токен гостя — в службе.
/// </summary>
public sealed class IpcClient : IIpcTransport, IAsyncDisposable
{
    /// <summary>Сколько ждать ответа службы (запись — 15 с × до 3 попыток тем же ключом идемпотентности).</summary>
    public static readonly TimeSpan CallTimeout = TimeSpan.FromSeconds(60);

    private readonly Func<CancellationToken, Task<Stream>> _connect;
    private readonly ConcurrentDictionary<ulong, TaskCompletionSource<Response>> _pending = new();
    private readonly CancellationTokenSource _stop = new();
    private Channel<Envelope>? _outbox;
    private long _nextId;
    private volatile bool _connected;

    public IpcClient(Func<CancellationToken, Task<Stream>> connect)
    {
        _connect = connect ?? throw new ArgumentNullException(nameof(connect));
    }

    public bool IsConnected => _connected;

    public event Action<Event>? EventReceived;

    public event Action<bool>? ConnectionChanged;

    /// <summary>Подключение к именованному каналу службы (Windows): служба проверяет SID, сеанс и путь exe клиента.</summary>
    [SupportedOSPlatform("windows")]
    public static Func<CancellationToken, Task<Stream>> NamedPipe(string pipeName) => async ct =>
    {
        // Identification: служба узнаёт SID клиента, но не может действовать от его имени.
        var pipe = new NamedPipeClientStream(".", pipeName, PipeDirection.InOut, PipeOptions.Asynchronous, TokenImpersonationLevel.Identification);
        try
        {
            await pipe.ConnectAsync(TimeSpan.FromSeconds(5), ct).ConfigureAwait(false);
            return pipe;
        }
        catch
        {
            await pipe.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    };

    /// <summary>Подключение к unix socket службы (Linux, тесты).</summary>
    public static Func<CancellationToken, Task<Stream>> UnixSocket(string path) => async ct =>
    {
        var socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        try
        {
            await socket.ConnectAsync(new UnixDomainSocketEndPoint(path), ct).ConfigureAwait(false);
            return new NetworkStream(socket, ownsSocket: true);
        }
        catch
        {
            socket.Dispose();
            throw;
        }
    };

    /// <summary>Пауза перед повторным подключением: 0,5 с, 1 с, 2 с … до 5 с.</summary>
    public static TimeSpan ReconnectDelay(int failures) =>
        TimeSpan.FromMilliseconds(Math.Min(5000, 500 * Math.Pow(2, Math.Clamp(failures, 0, 4))));

    /// <summary>Держит канал открытым до отмены: подключение, чтение, переподключение.</summary>
    public async Task RunAsync(CancellationToken cancellationToken)
    {
        using var linked = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, _stop.Token);
        var ct = linked.Token;
        var failures = 0;
        while (!ct.IsCancellationRequested)
        {
            Stream stream;
            try
            {
                stream = await _connect(ct).ConfigureAwait(false);
            }
            catch (Exception e) when (e is IOException or TimeoutException or SocketException or UnauthorizedAccessException)
            {
                await DelayAsync(ReconnectDelay(failures++), ct).ConfigureAwait(false);
                continue;
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
                break;
            }

            failures = 0;
            await ServeAsync(stream, ct).ConfigureAwait(false);
            if (!ct.IsCancellationRequested) await DelayAsync(ReconnectDelay(failures++), ct).ConfigureAwait(false);
        }
    }

    private static async Task DelayAsync(TimeSpan delay, CancellationToken ct)
    {
        try
        {
            await Task.Delay(delay, ct).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
        }
    }

    private async Task ServeAsync(Stream stream, CancellationToken ct)
    {
        await using var _ = stream.ConfigureAwait(false);
        using var session = CancellationTokenSource.CreateLinkedTokenSource(ct);
        var outbox = Channel.CreateBounded<Envelope>(new BoundedChannelOptions(64) { SingleReader = true, FullMode = BoundedChannelFullMode.Wait });
        _outbox = outbox;
        var writer = Task.Run(async () =>
        {
            await foreach (var env in outbox.Reader.ReadAllAsync(session.Token).ConfigureAwait(false))
                await IpcFraming.WriteAsync(stream, env, session.Token).ConfigureAwait(false);
        }, CancellationToken.None);
        SetConnected(true);
        try
        {
            while (!session.IsCancellationRequested)
            {
                var env = await IpcFraming.ReadAsync(stream, session.Token).ConfigureAwait(false);
                if (env is null) break;
                switch (env.KindCase)
                {
                    case Envelope.KindOneofCase.Response:
                        if (_pending.TryRemove(env.Response.Id, out var tcs)) tcs.TrySetResult(env.Response);
                        break;
                    case Envelope.KindOneofCase.Event:
                        EventReceived?.Invoke(env.Event);
                        break;
                }
            }
        }
        catch (Exception e) when (e is IOException or InvalidDataException or EndOfStreamException or OperationCanceledException or ObjectDisposedException)
        {
        }
        finally
        {
            _outbox = null;
            outbox.Writer.TryComplete();
            await session.CancelAsync().ConfigureAwait(false);
            try
            {
                await writer.ConfigureAwait(false);
            }
            catch (Exception e) when (e is IOException or OperationCanceledException or ObjectDisposedException or ChannelClosedException)
            {
            }

            FailPending();
            SetConnected(false);
        }
    }

    private void SetConnected(bool connected)
    {
        if (_connected == connected) return;
        _connected = connected;
        ConnectionChanged?.Invoke(connected);
    }

    private void FailPending()
    {
        foreach (var id in _pending.Keys)
        {
            if (_pending.TryRemove(id, out var tcs)) tcs.TrySetException(new IOException("The agent service connection was closed."));
        }
    }

    public async Task<Response> CallAsync(string method, ByteString payload, CancellationToken cancellationToken, TimeSpan? timeout = null)
    {
        ArgumentException.ThrowIfNullOrEmpty(method);
        var outbox = _outbox ?? throw new IOException("The agent service is not connected.");
        var id = (ulong)Interlocked.Increment(ref _nextId);
        var tcs = new TaskCompletionSource<Response>(TaskCreationOptions.RunContinuationsAsynchronously);
        _pending[id] = tcs;
        using var deadline = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        deadline.CancelAfter(timeout ?? CallTimeout);
        try
        {
            await outbox.Writer.WriteAsync(new Envelope { Request = new Request { Id = id, Method = method, Payload = payload ?? ByteString.Empty } }, deadline.Token).ConfigureAwait(false);
            return await tcs.Task.WaitAsync(deadline.Token).ConfigureAwait(false);
        }
        catch (ChannelClosedException e)
        {
            throw new IOException("The agent service connection was closed.", e);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            throw new TimeoutException("The agent service did not answer in time.");
        }
        finally
        {
            _pending.TryRemove(id, out _);
        }
    }

    public async ValueTask DisposeAsync()
    {
        await _stop.CancelAsync().ConfigureAwait(false);
        _stop.Dispose();
        FailPending();
    }
}
