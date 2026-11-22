using System.Threading.Channels;
using Google.Protobuf.WellKnownTypes;
using Grpc.Core;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Enrollment;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Guest;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.Core.Journal;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Core.Transport;
using Zafiro.Agent.Core.Updates;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Runtime;

/// <summary>
/// Связь службы агента с сервером (docs/agent-protocol.md §2, «Клиент .NET»): регистрация, стрим Connect с
/// переподключением (full jitter, RetryInfo, Drain), Hello/Welcome, команды с дедупликацией и журналом на диске,
/// Status по интервалу и при изменениях, события «как минимум один раз», продление сертификата, unary-вызовы гостя.
/// Платформенные действия — через <see cref="IPlatform"/>; UI получает <see cref="UiEvent"/> (служба пересылает по IPC).
/// </summary>
public sealed class AgentRuntime : IGuestEvents, IAsyncDisposable
{
    /// <summary>Техрежим по умолчанию, минут (клуб не задал staff_minutes). Laravel: app/Services/ClubComputerSessions.php DEFAULT_STAFF_MINUTES.</summary>
    public const uint DefaultStaffMinutes = 15;

    private readonly AgentOptions _options;
    private readonly ICredentialStore _store;
    private readonly IPlatform _platform;
    private readonly AgentChannelFactory _channels;
    private readonly Enroller _enroller;
    private readonly UpdateVerifier _updates;
    private readonly UpdateInstaller? _installer;
    private readonly TimeProvider _time;
    private readonly Random _random;
    private readonly ILogger _log;
    private readonly SemaphoreSlim _wake = new(0, int.MaxValue);
    private readonly SemaphoreSlim _enrollGate = new(1, 1);
    private readonly Channel<bool> _statusSignal = Channel.CreateBounded<bool>(new BoundedChannelOptions(1) { FullMode = BoundedChannelFullMode.DropWrite });
    private readonly object _gate = new();
    private readonly string _bootId;
    private readonly DeviceInfo _device;
    private readonly HealthMonitor _health;
    private readonly HealthGate _healthGate = new();
    private readonly ShellThemeStore _shellTheme;

    private AgentCredentials? _creds;
    private AgentChannel? _channel;
    private Channel<AgentMessage>? _outbox;
    private CancellationTokenSource? _streamCts;
    private bool _rotationPending;
    private DateTimeOffset? _renewAt;
    private DateTimeOffset? _afterSessionAt;
    private AfterSessionAction _afterSessionAction;
    private int _updateBusy;
    private bool _renewing;

    public AgentRuntime(AgentOptions options, ICredentialStore store, IPlatform platform, TrustAnchors trust, UpdateVerifier updates,
        UpdateInstaller? installer, ILoggerFactory loggers, TimeProvider? time = null, Random? random = null)
    {
        ArgumentNullException.ThrowIfNull(loggers);
        _options = options ?? throw new ArgumentNullException(nameof(options));
        _store = store ?? throw new ArgumentNullException(nameof(store));
        _platform = platform ?? throw new ArgumentNullException(nameof(platform));
        _updates = updates ?? throw new ArgumentNullException(nameof(updates));
        _installer = installer;
        _time = time ?? TimeProvider.System;
        _random = random ?? Random.Shared;
        _log = loggers.CreateLogger("Zafiro.Agent.Runtime");
        State = new AgentState(_time) { AgentVersion = options.AgentVersion, FallbackLocale = options.Locale };
        var state = State;
        _channels = new AgentChannelFactory(options, trust, () => state.RequestLocale);
        _enroller = new Enroller(options, _channels, trust, store, _time, loggers.CreateLogger("Zafiro.Agent.Enroll"));
        string? DataFile(string name) => string.IsNullOrEmpty(options.DataDirectory) ? null : Path.Combine(options.DataDirectory, name);
        Commands = new CommandJournalStore(DataFile("commands.pb"));
        Events = new EventJournalStore(DataFile("events.pb"));
        _shellTheme = new ShellThemeStore(DataFile("shell-theme.pb"));
        State.RestoreShellTheme(_shellTheme.Saved);
        Guest = new GuestApi(() => _channel?.Invoker, State, options, _time, this);
        _bootId = platform.GetBootId();
        _device = platform.GetDeviceInfo(options.AgentVersion, options.Diskless);
        State.Changed += OnStateChanged;
        var collector = new HealthCollector(platform.Health ?? NullHealthProbe.Instance, _time, loggers.CreateLogger("Zafiro.Agent.Health"),
            new HealthCollectorOptions { Diskless = options.Diskless });
        _health = new HealthMonitor(collector, () => HealthCollectInterval, ServerHost, options.HealthPollInterval, options.HealthSourceTimeout, _time,
            loggers.CreateLogger("Zafiro.Agent.Health"));
        _health.Urgent += OnHealthUrgent;
    }

    /// <summary>Здоровье ПК (последний снимок) — для тестов и диагностики.</summary>
    public HealthMonitor Health => _health;

    /// <summary>Status ушёл в стрим (для тестов).</summary>
    internal event Action<Zafiro.Agent.V1.Status>? StatusSent;

    /// <summary>Состояние ПК (для UI и тестов).</summary>
    public AgentState State { get; }

    /// <summary>Вызовы гостя (вход, тарифы, покупки, магазин, друзья…).</summary>
    public GuestApi Guest { get; }

    public CommandJournalStore Commands { get; }

    public EventJournalStore Events { get; }

    /// <summary>Сведения о ПК, отправляемые в Enroll/Hello.</summary>
    public DeviceInfo Device => _device.Clone();

    /// <summary>Текущие учётные данные (null — не зарегистрирован).</summary>
    public AgentCredentials? Credentials
    {
        get
        {
            lock (_gate) return _creds;
        }
    }

    /// <summary>Событие для UI (снимок, сообщение сотрудника, чат…). Вызывается из потоков службы.</summary>
    public event Action<Event>? UiEvent;

    /// <summary>Команда выполнена (для журнала службы и тестов).</summary>
    public event Action<Command, CommandResult>? CommandHandled;

    // ------------------------------------------------------------------ регистрация

    /// <summary>Регистрация по коду, введённому сотрудником на экране агента. Ошибки — <see cref="AgentRpcException"/>.</summary>
    public async Task<Snapshot> EnrollWithCodeAsync(string code, CancellationToken cancellationToken)
    {
        await EnrollAsync(EnrollSecret.FromCode(code), cancellationToken).ConfigureAwait(false);
        return State.BuildSnapshot();
    }

    private async Task EnrollAsync(EnrollSecret secret, CancellationToken cancellationToken)
    {
        if (!await _enrollGate.WaitAsync(0, cancellationToken).ConfigureAwait(false))
            throw AgentRpcException.Local(StatusCode.Aborted, ErrorCode.Unspecified, "enrollment is already in progress");
        var before = State.Connection;
        try
        {
            State.SetConnection(Connection.Enrolling);
            var creds = await _enroller.EnrollAsync(secret, _device, _ => State.SetConnection(Connection.PendingApproval), cancellationToken).ConfigureAwait(false);
            ReplaceCredentials(creds);
            State.SetConnection(Connection.Connecting);
            _wake.Release();
        }
        catch
        {
            if (State.Connection is Connection.Enrolling or Connection.PendingApproval) State.SetConnection(before is Connection.Revoked ? Connection.Revoked : Connection.NotEnrolled);
            throw;
        }
        finally
        {
            _enrollGate.Release();
        }
    }

    private void ReplaceCredentials(AgentCredentials? creds)
    {
        AgentCredentials? old;
        lock (_gate)
        {
            old = _creds;
            _creds = creds;
            _renewAt = null;
            _rotationPending = creds is not null && !string.IsNullOrEmpty(creds.Stored.PreviousKeyName);
        }

        // Старые данные освобождаются после закрытия канала, который ими пользовался.
        if (old is not null && !ReferenceEquals(old, creds)) _ = DisposeLater(old);
    }

    private async Task DisposeLater(AgentCredentials old)
    {
        try
        {
            await Task.Delay(TimeSpan.FromSeconds(30), _time).ConfigureAwait(false);
        }
        finally
        {
            old.Dispose();
        }
    }

    // ------------------------------------------------------------------ основной цикл

    /// <summary>Держит связь, пока не отменён <paramref name="cancellationToken"/>.</summary>
    public async Task RunAsync(CancellationToken cancellationToken)
    {
        var housekeeping = Task.Run(() => HousekeepingLoop(cancellationToken), CancellationToken.None);
        var health = Task.Run(() => _health.RunAsync(cancellationToken), CancellationToken.None);
        var attempt = 0;
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                var creds = LoadCredentials();
                if (creds is null)
                {
                    attempt = await WaitForEnrollmentAsync(attempt, cancellationToken).ConfigureAwait(false);
                    continue;
                }

                State.SetConnection(Connection.Connecting);
                var started = _time.GetUtcNow();
                if (await ConnectOnceAsync(creds, cancellationToken).ConfigureAwait(false) is not { } end) continue;
                if (cancellationToken.IsCancellationRequested) break;
                if (_time.GetUtcNow() - started >= Backoff.StableAfter) attempt = 0;
                var delay = end.Forced ?? Backoff.Next(attempt++, _random, end.RetryAfter);
                State.SetConnection(Connection.Connecting);
                await Delay(delay, cancellationToken).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
        }
        finally
        {
            await IgnoreCancel(housekeeping).ConfigureAwait(false);
            await IgnoreCancel(health).ConfigureAwait(false);
        }
    }

    /// <summary>Учётные данные в памяти или на диске; истёкший сертификат удаляется (нужна регистрация).</summary>
    private AgentCredentials? LoadCredentials()
    {
        var creds = Credentials;
        if (creds is null)
        {
            creds = _store.Load();
            if (creds is not null) ReplaceCredentials(creds);
        }

        if (creds is null || creds.NotAfter > _time.GetUtcNow()) return creds;
        _log.LogWarning("certificate expired at {NotAfter}: enrollment is required", creds.NotAfter);
        ForgetCredentials();
        return null;
    }

    /// <summary>Ключ и сертификат удаляются с диска и из памяти: нужна новая регистрация.</summary>
    private void ForgetCredentials()
    {
        _store.Clear();
        ReplaceCredentials(null);
    }

    /// <summary>
    /// ПК не зарегистрирован: регистрация по ключу образа (бездисковые ПК; ошибка — пауза по <paramref name="attempt"/>)
    /// или ожидание кода с экрана агента. Возвращает номер следующей попытки.
    /// </summary>
    private async Task<int> WaitForEnrollmentAsync(int attempt, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(_options.ImageKey))
        {
            if (State.Connection != Connection.Revoked) State.SetConnection(Connection.NotEnrolled);
            await _wake.WaitAsync(cancellationToken).ConfigureAwait(false);
            return attempt;
        }

        try
        {
            await EnrollAsync(EnrollSecret.FromImageKey(_options.ImageKey), cancellationToken).ConfigureAwait(false);
            return 0;
        }
        catch (Exception ex) when (ex is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
        {
            var retry = (ex as AgentRpcException)?.RetryAfter;
            _log.LogWarning("enroll by image key failed: {Error}", ex.Message);
            await Delay(Backoff.Next(attempt++, _random, retry), cancellationToken).ConfigureAwait(false);
            return attempt;
        }
    }

    /// <summary>Чем закончился стрим: пауза задана (Drain, новый сертификат, версия протокола) или по RetryInfo сервера.</summary>
    private readonly record struct StreamEnd(TimeSpan? Forced, TimeSpan? RetryAfter);

    /// <summary>Один стрим до закрытия. null — сертификат отозван или истёк: сразу к регистрации, без паузы.</summary>
    private async Task<StreamEnd?> ConnectOnceAsync(AgentCredentials creds, CancellationToken cancellationToken)
    {
        try
        {
            await RunStreamAsync(creds, cancellationToken).ConfigureAwait(false);
            return new StreamEnd(Interlocked.Exchange(ref _drainBox, null)?.Value, null);
        }
        catch (CertificateRevokedException)
        {
            _log.LogWarning("certificate revoked: key and certificate removed, a new enrollment code is required");
            ForgetCredentials();
            State.SignOut();
            State.SetConnection(Connection.Revoked);
            return null;
        }
        catch (CertificateExpiredException e)
        {
            _log.LogWarning("certificate rejected ({Code}): enrollment is required", ErrorCodes.Name(e.ErrorCode));
            ForgetCredentials();
            State.SetConnection(Connection.NotEnrolled);
            return null;
        }
        catch (ProtocolVersionUnsupportedException)
        {
            _log.LogError("protocol version is not supported by the server: waiting for an update");
            return new StreamEnd(Backoff.ProtocolUnsupported, null);
        }
        catch (AgentRpcException e) when (!cancellationToken.IsCancellationRequested)
        {
            if (e.ErrorCode == ErrorCode.ComputerNotFound)
            {
                _log.LogWarning("computer card was deleted in the club panel");
            }

            _log.LogInformation("stream closed: {Error}", e.Message);
            return new StreamEnd(null, e.RetryAfter);
        }
        catch (Exception ex) when (!cancellationToken.IsCancellationRequested && ex is not OperationCanceledException)
        {
            _log.LogInformation("stream failed: {Error}", ex.Message);
            return new StreamEnd(null, null);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            return new StreamEnd(Interlocked.Exchange(ref _drainBox, null)?.Value, null);
        }
    }

    private sealed record DelayBox(TimeSpan Value);

    private DelayBox? _drainBox;

    private async Task Delay(TimeSpan delay, CancellationToken cancellationToken)
    {
        if (delay <= TimeSpan.Zero) return;
        // Регистрация (новые учётные данные) прерывает ожидание.
        using var cts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        var wake = _wake.WaitAsync(cts.Token);
        var done = await Task.WhenAny(Task.Delay(delay, _time, cts.Token), wake).ConfigureAwait(false);
        if (done != wake) await cts.CancelAsync().ConfigureAwait(false);
        await IgnoreCancel(wake).ConfigureAwait(false);
        cancellationToken.ThrowIfCancellationRequested();
    }

    private Hello BuildHello() => new()
    {
        ProtocolVersion = AgentChannelFactory.ProtocolVersion,
        AgentVersion = _options.AgentVersion,
        LastCommandSeq = Commands.LastCommandSeq,
        BootId = _bootId,
        Device = _device.Clone(),
        Capabilities = _platform.Capabilities.Clone(),
        Status = State.BuildStatus(_platform.GetLoad()),
        TranslationsVersion = State.TranslationsVersion,
        Locale = _options.Locale,
    };

    private async Task RunStreamAsync(AgentCredentials creds, CancellationToken cancellationToken)
    {
        using var streamCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        var channel = _channels.CreateChannel(creds);
        var outbox = Channel.CreateUnbounded<AgentMessage>(new UnboundedChannelOptions { SingleReader = true });
        lock (_gate)
        {
            _channel = channel;
            _streamCts = streamCts;
        }

        Task? writer = null;
        Task? status = null;
        try
        {
            var client = new AgentService.AgentServiceClient(channel.Invoker);
            using var call = client.Connect(cancellationToken: streamCts.Token);
            writer = WriteLoop(call.RequestStream, outbox.Reader, streamCts.Token);
            outbox.Writer.TryWrite(new AgentMessage { Hello = BuildHello() });
            OnWelcome(await ReceiveWelcomeAsync(call.ResponseStream, streamCts.Token).ConfigureAwait(false), creds, outbox);
            status = StatusLoop(outbox.Writer, streamCts.Token);
            while (await MoveNext(call.ResponseStream, streamCts.Token).ConfigureAwait(false))
            {
                var m = call.ResponseStream.Current;
                switch (m.PayloadCase)
                {
                    case ServerMessage.PayloadOneofCase.Command:
                        await HandleCommandAsync(m.Command, streamCts.Token).ConfigureAwait(false);
                        break;
                    case ServerMessage.PayloadOneofCase.EventAck:
                        Events.Ack(m.EventAck.EventId);
                        break;
                    case ServerMessage.PayloadOneofCase.Welcome:
                        State.ApplyWelcome(m.Welcome);
                        break;
                }
            }
        }
        finally
        {
            lock (_gate)
            {
                _outbox = null;
                _channel = null;
                _streamCts = null;
            }

            outbox.Writer.TryComplete();
            await streamCts.CancelAsync().ConfigureAwait(false);
            foreach (var t in new[] { writer, status })
            {
                if (t is null) continue;
                try
                {
                    await t.ConfigureAwait(false);
                }
                catch (Exception ex) when (ex is OperationCanceledException or RpcException or InvalidOperationException or IOException)
                {
                }
            }

            channel.Dispose();
        }
    }

    /// <summary>Первое сообщение сервера — Welcome, не позже <see cref="AgentOptions.WelcomeTimeout"/>.</summary>
    private async Task<Welcome> ReceiveWelcomeAsync(IAsyncStreamReader<ServerMessage> stream, CancellationToken ct)
    {
        using var welcomeCts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        welcomeCts.CancelAfter(_options.WelcomeTimeout);
        if (!await MoveNext(stream, welcomeCts.Token).ConfigureAwait(false))
            throw AgentRpcException.Local(StatusCode.Unavailable, ErrorCode.Unspecified, "stream closed before Welcome");
        return stream.Current.Welcome
            ?? throw AgentRpcException.Local(StatusCode.Internal, ErrorCode.Unspecified, "the first server message is not Welcome");
    }

    /// <summary>Стрим открыт: состояние из Welcome, отправка Status, неподтверждённых событий и недоставленных итогов.</summary>
    private void OnWelcome(Welcome welcome, AgentCredentials creds, Channel<AgentMessage> outbox)
    {
        State.ApplyWelcome(welcome);
        _log.LogInformation("connected: computer {ComputerId}, club {ClubId}, latest seq {Seq} (ours {Ours})",
            welcome.ComputerId, welcome.ClubId, welcome.LatestCommandSeq, Commands.LastCommandSeq);
        lock (_gate) _outbox = outbox;
        if (_rotationPending)
        {
            // Первое подключение с новым сертификатом удалось — прежний ключ больше не нужен (§12.6 п. 6).
            _store.CommitRotation();
            creds.Stored.PreviousKeyName = "";
            _rotationPending = false;
            _log.LogInformation("connected with the renewed certificate, previous key removed");
        }

        _healthGate.Reset(); // первый Status нового стрима — с health
        outbox.Writer.TryWrite(new AgentMessage { Status = NextStatus() });
        foreach (var ev in Events.Pending()) outbox.Writer.TryWrite(new AgentMessage { Event = ev });
        foreach (var r in Commands.Undelivered()) SendResult(r);
        if (welcome.TranslationsVersion > State.TranslationsVersion) RaiseUi(new Event { TranslationsUpdated = new TranslationsUpdated { Version = welcome.TranslationsVersion } });
        RefreshAutoBilling(); // мгновенные AutoBillingChanged, пока стрима не было, потеряны
    }

    private static async Task<bool> MoveNext(IAsyncStreamReader<ServerMessage> stream, CancellationToken ct)
    {
        try
        {
            return await stream.MoveNext(ct).ConfigureAwait(false);
        }
        catch (RpcException ex) when (ex.StatusCode != StatusCode.Cancelled || !ct.IsCancellationRequested)
        {
            throw RpcErrors.Parse(ex);
        }
        catch (RpcException) when (ct.IsCancellationRequested)
        {
            throw new OperationCanceledException(ct);
        }
    }

    private static async Task WriteLoop(IClientStreamWriter<AgentMessage> stream, ChannelReader<AgentMessage> reader, CancellationToken ct)
    {
        await foreach (var m in reader.ReadAllAsync(ct).ConfigureAwait(false))
        {
            await stream.WriteAsync(m, ct).ConfigureAwait(false);
        }
    }

    /// <summary>Полный снимок здоровья — раз в status_interval, но не чаще 30 с и не реже 10 мин (бюджет CPU, §14.10).</summary>
    private TimeSpan HealthCollectInterval => TimeSpan.FromTicks(Math.Clamp(State.StatusInterval.Ticks, TimeSpan.FromSeconds(30).Ticks, TimeSpan.FromMinutes(10).Ticks));

    /// <summary>Хост сервера стрима — по нему выбирается сетевой интерфейс для link_speed_mbps.</summary>
    private string? ServerHost()
    {
        var endpoint = Credentials?.Stored.AgentEndpoint;
        if (string.IsNullOrEmpty(endpoint)) endpoint = _options.EnrollEndpoint;
        try
        {
            return AgentChannelFactory.ToAddress(endpoint).Host;
        }
        catch (Exception ex) when (ex is ArgumentException or UriFormatException)
        {
            return null;
        }
    }

    /// <summary>Периферия или SMART изменились — Status с health сразу (не чаще 1 в 2 с, как любой Status).</summary>
    private void OnHealthUrgent()
    {
        _healthGate.MarkUrgent();
        _statusSignal.Writer.TryWrite(true);
    }

    /// <summary>Status для стрима: health — по <see cref="HealthGate"/> (раз в интервал, сразу при срочном изменении).</summary>
    internal Zafiro.Agent.V1.Status NextStatus()
    {
        var status = State.BuildStatus(_platform.GetLoad());
        var interval = State.StatusInterval;
        var health = _healthGate.Take(_health.Latest, _time.GetUtcNow(), interval, HealthCollectInterval * 3);
        if (health is not null) status.Health = health;
        StatusSent?.Invoke(status);
        return status;
    }

    /// <summary>Status: сразу после Welcome, при изменении состояния (не чаще 1 в 2 с) и не реже status_interval.</summary>
    private async Task StatusLoop(ChannelWriter<AgentMessage> writer, CancellationToken ct)
    {
        var minGap = TimeSpan.FromSeconds(2);
        var last = _time.GetUtcNow();
        while (!ct.IsCancellationRequested)
        {
            using (var wait = CancellationTokenSource.CreateLinkedTokenSource(ct))
            {
                var signal = _statusSignal.Reader.WaitToReadAsync(wait.Token).AsTask();
                var tick = Task.Delay(State.StatusInterval, _time, wait.Token);
                await Task.WhenAny(signal, tick).ConfigureAwait(false);
                await wait.CancelAsync().ConfigureAwait(false);
                await IgnoreCancel(signal).ConfigureAwait(false);
                await IgnoreCancel(tick).ConfigureAwait(false);
            }

            ct.ThrowIfCancellationRequested();
            _statusSignal.Reader.TryRead(out _);
            var gap = _time.GetUtcNow() - last;
            if (gap < minGap) await Task.Delay(minGap - gap, _time, ct).ConfigureAwait(false);
            writer.TryWrite(new AgentMessage { Status = NextStatus() });
            last = _time.GetUtcNow();
        }
    }

    private static async Task IgnoreCancel(Task t)
    {
        try
        {
            await t.ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
        }
    }

    private void OnStateChanged()
    {
        _statusSignal.Writer.TryWrite(true);
        RememberShellTheme();
        RaiseUi(new Event { Snapshot = State.BuildSnapshot() });
    }

    /// <summary>Тема клуба сменилась (Welcome, ConfigChanged) — на диск, чтобы после перезагрузки ПК экран входа был в ней.</summary>
    private void RememberShellTheme()
    {
        try
        {
            _shellTheme.Save(State.ShellTheme);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            _log.LogWarning("shell theme not saved: {Error}", ex.Message);
        }
    }

    private void RaiseUi(Event e)
    {
        try
        {
            UiEvent?.Invoke(e);
        }
        catch (Exception ex) when (ex is InvalidOperationException or IOException or ObjectDisposedException)
        {
            _log.LogDebug("ui event handler failed: {Error}", ex.Message);
        }
    }

    // ------------------------------------------------------------------ события агента

    /// <summary>Событие «как минимум один раз»: журнал на диске до EventAck, отправка сразу, если стрим открыт.</summary>
    public AgentEvent RaiseEvent(Action<AgentEvent> fill)
    {
        ArgumentNullException.ThrowIfNull(fill);
        var ev = new AgentEvent
        {
            EventId = Guid.CreateVersion7().ToString(),
            OccurredAt = Timestamp.FromDateTimeOffset(State.ServerNow()),
        };
        fill(ev);
        Events.Add(ev);
        Channel<AgentMessage>? outbox;
        lock (_gate) outbox = _outbox;
        outbox?.Writer.TryWrite(new AgentMessage { Event = ev });
        return ev;
    }

    void IGuestEvents.SignedIn(GuestSignIn signIn, SignInMethod method)
    {
        RaiseEvent(e => e.GuestSignedIn = new GuestSignedIn { UserId = signIn.User?.UserId ?? 0, Method = method });
        RefreshAutoBilling();
    }

    /// <summary>
    /// Состояние автобиллинга гостя с сервера (после входа и переподключения; дальше — команда AutoBillingChanged).
    /// Автобиллинг решает и покупает сервер (решение Q-008); служба только не выводит гостя по бездействию, пока он ждёт.
    /// </summary>
    private void RefreshAutoBilling()
    {
        if (State.GuestToken is null) return;
        _ = Task.Run(async () =>
        {
            try
            {
                await Guest.GetAutoBillingAsync(new GetAutoBillingRequest(), CancellationToken.None).ConfigureAwait(false);
            }
            catch (AgentRpcException ex)
            {
                _log.LogInformation("auto billing state not loaded: {Code}", ex.ErrorCode);
            }
        });
    }

    void IGuestEvents.SignedOut(long userId, SignOutReason reason) =>
        RaiseEvent(e => e.GuestSignedOut = new GuestSignedOut { UserId = userId, Reason = reason });

    /// <summary>Активность гостя от UI (выход по бездействию считается по ней).</summary>
    public void ReportActivity(uint idleSeconds) => State.SetIdle(idleSeconds);

    // ------------------------------------------------------------------ команды

    private void SendResult(CommandResult r)
    {
        Channel<AgentMessage>? outbox;
        lock (_gate) outbox = _outbox;
        if (outbox is not null && outbox.Writer.TryWrite(new AgentMessage { CommandResult = r }))
        {
            Commands.Delivered(r.CommandId, r.Status);
        }
    }

    internal async Task<CommandResult> HandleCommandAsync(Command cmd, CancellationToken ct)
    {
        if (Commands.Check(cmd, out var saved) == CommandCheck.Duplicate)
        {
            _log.LogDebug("command {CommandId} (seq {Seq}) is a duplicate", cmd.CommandId, cmd.Seq);
            SendResult(saved!);
            CommandHandled?.Invoke(cmd, saved!);
            return saved!;
        }

        CommandOutcome outcome;
        if (cmd.ExpiresAt is not null && cmd.ExpiresAt.ToDateTimeOffset() <= State.ServerNow())
        {
            outcome = CommandOutcome.Fail(ErrorCode.CommandExpired, "command expired");
        }
        else
        {
            try
            {
                outcome = await ExecuteAsync(cmd, ct).ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                _log.LogWarning("command {Kind} failed: {Error}", cmd.KindCase, ex.Message);
                outcome = CommandOutcome.Fail(ErrorCode.CommandRejected, Truncate(ex.Message));
            }
        }

        var now = _time.GetUtcNow();
        var result = new CommandResult
        {
            CommandId = cmd.CommandId,
            Seq = cmd.Seq,
            Status = outcome.Status,
            ErrorCode = outcome.Error,
            Message = outcome.Message ?? "",
            CompletedAt = Timestamp.FromDateTimeOffset(now),
        };
        if (cmd.KindCase == Command.KindOneofCase.Ping) result.AgentTime = Timestamp.FromDateTimeOffset(now);
        // Журнал (last_command_seq) — на диск ДО отправки итога (agent.proto п. 6).
        Commands.Record(result);
        SendResult(result);
        CommandHandled?.Invoke(cmd, result);
        if (outcome.After is not null) _ = RunAfter(outcome.After);
        return result;
    }

    private async Task RunAfter(Func<Task> after)
    {
        try
        {
            await after().ConfigureAwait(false);
        }
        catch (Exception ex) when (ex is not OperationCanceledException)
        {
            _log.LogWarning("command continuation failed: {Error}", ex.Message);
        }
    }

    private static string Truncate(string s) => s.Length <= 500 ? s : s[..500];

    private async Task<CommandOutcome> ExecuteAsync(Command cmd, CancellationToken ct)
    {
        switch (cmd.KindCase)
        {
            case Command.KindOneofCase.StartSession:
                if (cmd.StartSession.Session is null) return CommandOutcome.Fail(ErrorCode.CommandRejected, "session is empty");
                State.StartSession(cmd.StartSession.Session);
                _afterSessionAt = null;
                return CommandOutcome.Ok();

            case Command.KindOneofCase.EndSession:
                if (!State.EndSession(cmd.EndSession.SessionId)) return CommandOutcome.Fail(ErrorCode.SessionMismatch, "another session is active");
                ScheduleAfterSession();
                return CommandOutcome.Ok();

            case Command.KindOneofCase.Lock:
                State.Lock(cmd.Lock.Reason);
                return CommandOutcome.Ok();

            case Command.KindOneofCase.Unlock:
                return State.Unlock() ? CommandOutcome.Ok() : CommandOutcome.Fail(ErrorCode.CommandRejected, "nothing to unlock");

            case Command.KindOneofCase.Reboot:
            case Command.KindOneofCase.Shutdown:
                return PowerCommand(cmd);

            case Command.KindOneofCase.ShowMessage:
            {
                if (!_platform.Capabilities.ShowMessage) return CommandOutcome.Fail(ErrorCode.CommandUnsupported);
                var msg = cmd.ShowMessage.Clone();
                if (msg.Text.Length > 300) msg.Text = msg.Text[..300];
                RaiseUi(new Event { ShowMessage = msg });
                return CommandOutcome.Ok();
            }

            case Command.KindOneofCase.BalanceChanged:
                if (cmd.BalanceChanged.Balance is not null) State.SetBalance(cmd.BalanceChanged.UserId, cmd.BalanceChanged.Balance);
                return CommandOutcome.Ok();

            case Command.KindOneofCase.UpdateAvailable:
                return StartUpdate(cmd);

            case Command.KindOneofCase.TranslationsChanged:
                State.SetServerTranslationsVersion(cmd.TranslationsChanged.Version);
                if (cmd.TranslationsChanged.Version > State.TranslationsVersion)
                    RaiseUi(new Event { TranslationsUpdated = new TranslationsUpdated { Version = cmd.TranslationsChanged.Version } });
                return CommandOutcome.Ok();

            case Command.KindOneofCase.ConfigChanged:
                if (cmd.ConfigChanged.Config is not null) State.ApplyConfig(cmd.ConfigChanged.Config);
                return CommandOutcome.Ok();

            case Command.KindOneofCase.Ping:
                if (cmd.Ping.ServerTime is not null) State.SyncClock(cmd.Ping.ServerTime);
                return CommandOutcome.Ok();

            case Command.KindOneofCase.EnterServiceMode:
            {
                if (!_platform.Capabilities.ServiceMode) return CommandOutcome.Fail(ErrorCode.CommandUnsupported);
                var minutes = cmd.EnterServiceMode.StaffMinutes > 0 ? cmd.EnterServiceMode.StaffMinutes : State.Config?.StaffMinutes ?? 0;
                if (minutes == 0) minutes = DefaultStaffMinutes;
                EnterServiceMode(_time.GetUtcNow() + TimeSpan.FromMinutes(minutes));
                return CommandOutcome.Ok();
            }

            case Command.KindOneofCase.WakeOnLan:
            {
                if (!_platform.Capabilities.WakeOnLan) return CommandOutcome.Fail(ErrorCode.CommandUnsupported);
                var macs = cmd.WakeOnLan.MacAddresses.Take(50).ToList();
                await _platform.WakeOnLanAsync(macs, ct).ConfigureAwait(false);
                return CommandOutcome.Ok();
            }

            case Command.KindOneofCase.SignOutGuest:
            {
                var user = State.SignOut(cmd.SignOutGuest.UserId);
                if (user > 0) RaiseEvent(e => e.GuestSignedOut = new GuestSignedOut { UserId = user, Reason = SignOutReason.Server });
                return CommandOutcome.Ok();
            }

            case Command.KindOneofCase.GuestCountersChanged:
                if (cmd.GuestCountersChanged.UserId == State.SignedInUserId) RaiseUi(new Event { GuestCounters = cmd.GuestCountersChanged });
                return CommandOutcome.Ok();

            case Command.KindOneofCase.ChatMessageReceived:
                if (cmd.ChatMessageReceived.UserId == State.SignedInUserId) RaiseUi(new Event { ChatMessage = cmd.ChatMessageReceived });
                return CommandOutcome.Ok();

            case Command.KindOneofCase.QrLoginApproved:
                RaiseUi(new Event { QrLoginApproved = cmd.QrLoginApproved });
                return CommandOutcome.Ok();

            case Command.KindOneofCase.AutoBillingChanged:
                if (cmd.AutoBillingChanged.Status is not null) State.SetAutoBilling(cmd.AutoBillingChanged.UserId, cmd.AutoBillingChanged.Status);
                return CommandOutcome.Ok();

            case Command.KindOneofCase.Drain:
            {
                var delay = Backoff.Drain(cmd.Drain.ReconnectWithin?.ToTimeSpan(), _random);
                return CommandOutcome.Ok(() => ReconnectAfterAsync(delay));
            }

            default:
                return CommandOutcome.Fail(ErrorCode.CommandUnsupported, "unknown command");
        }
    }

    /// <summary>Перезагрузка или выключение по команде: итог сразу, само действие — после отправки итога.</summary>
    private CommandOutcome PowerCommand(Command cmd)
    {
        if (!_platform.Capabilities.PowerControl) return CommandOutcome.Fail(ErrorCode.CommandUnsupported);
        var reboot = cmd.KindCase == Command.KindOneofCase.Reboot;
        var delay = (reboot ? cmd.Reboot.Delay : cmd.Shutdown.Delay)?.ToTimeSpan() ?? TimeSpan.Zero;
        if (delay < TimeSpan.Zero) delay = TimeSpan.Zero;
        State.SetShuttingDown(true);
        return CommandOutcome.Ok(async () =>
        {
            if (!await PowerOffAsync(reboot, delay, CancellationToken.None).ConfigureAwait(false))
                RaiseEvent(e => e.Error = new AgentError { Component = "power", Message = "the operating system refused to " + (reboot ? "restart" : "shut down") });
        });
    }

    /// <summary>Перезагрузка или выключение ПК (состояние «выключается» уже выставлено); отказ ОС — состояние снимается.</summary>
    private async Task<bool> PowerOffAsync(bool reboot, TimeSpan delay, CancellationToken ct)
    {
        var ok = reboot
            ? await _platform.RebootAsync(delay, ct).ConfigureAwait(false)
            : await _platform.ShutdownAsync(delay, ct).ConfigureAwait(false);
        if (!ok) State.SetShuttingDown(false);
        return ok;
    }

    /// <summary>Закрыть текущий стрим: цикл переподключится через <paramref name="delay"/> (Drain, новый сертификат).</summary>
    private async Task ReconnectAfterAsync(TimeSpan delay)
    {
        Interlocked.Exchange(ref _drainBox, new DelayBox(delay));
        CancellationTokenSource? cts;
        lock (_gate) cts = _streamCts;
        if (cts is not null) await cts.CancelAsync().ConfigureAwait(false);
    }

    /// <summary>Техрежим (по команде или после PIN сотрудника): агент закрыт до <paramref name="until"/>.</summary>
    public void EnterServiceMode(DateTimeOffset until)
    {
        State.EnterService(until);
        RaiseEvent(e => e.ServiceModeChanged = new ServiceModeChanged { Active = true, Until = Timestamp.FromDateTimeOffset(until) });
    }

    private void ScheduleAfterSession()
    {
        var cfg = State.Config;
        if (cfg is null || cfg.AfterSessionAction is AfterSessionAction.Unspecified or AfterSessionAction.None) return;
        _afterSessionAction = cfg.AfterSessionAction;
        _afterSessionAt = _time.GetUtcNow() + (cfg.AfterSessionDelay?.ToTimeSpan() ?? TimeSpan.Zero);
    }

    private CommandOutcome StartUpdate(Command cmd)
    {
        var u = cmd.UpdateAvailable;
        if (!_platform.Capabilities.SelfUpdate || _installer is null) return CommandOutcome.Fail(ErrorCode.CommandUnsupported);
        var rejection = _updates.Verify(u);
        if (rejection != UpdateRejection.None)
        {
            _log.LogWarning("update {Version} rejected before download: {Reason}", u.Version, rejection);
            RaiseEvent(e => e.UpdateResult = new UpdateResult { Version = u.Version, Success = false, ErrorCode = ErrorCode.UpdateVerificationFailed });
            return CommandOutcome.Fail(ErrorCode.UpdateVerificationFailed, rejection.ToString());
        }

        if (Interlocked.Exchange(ref _updateBusy, 1) == 1) return CommandOutcome.Fail(ErrorCode.CommandRejected, "another update is in progress");
        var expires = cmd.ExpiresAt?.ToDateTimeOffset();
        return CommandOutcome.Accepted(async () =>
        {
            try
            {
                // Без install_now — только когда ПК свободен (никто не вошёл, нет сессии).
                while (!u.InstallNow && State.ComputerState != ComputerState.Idle)
                {
                    if (expires is { } e && State.ServerNow() >= e) break;
                    await Task.Delay(TimeSpan.FromSeconds(30), _time).ConfigureAwait(false);
                }

                (bool Ok, ErrorCode Error) r = (false, ErrorCode.CommandExpired);
                if (u.InstallNow || State.ComputerState == ComputerState.Idle)
                    r = await _installer.DownloadAndInstallAsync(u, CancellationToken.None).ConfigureAwait(false);
                var final = new CommandResult
                {
                    CommandId = cmd.CommandId,
                    Seq = cmd.Seq,
                    Status = r.Ok ? CommandStatus.Succeeded : CommandStatus.Failed,
                    ErrorCode = r.Ok ? ErrorCode.Unspecified : r.Error,
                    CompletedAt = Timestamp.FromDateTimeOffset(_time.GetUtcNow()),
                };
                Commands.Record(final);
                Commands.AddUndelivered(final);
                SendResult(final);
                if (!r.Ok) RaiseEvent(e => e.UpdateResult = new UpdateResult { Version = u.Version, Success = false, ErrorCode = r.Error });
            }
            finally
            {
                Volatile.Write(ref _updateBusy, 0);
            }
        });
    }

    // ------------------------------------------------------------------ фоновые проверки

    private async Task HousekeepingLoop(CancellationToken ct)
    {
        using var timer = new PeriodicTimer(_options.HousekeepingInterval, _time);
        while (await timer.WaitForNextTickAsync(ct).ConfigureAwait(false))
        {
            try
            {
                await HousekeepingOnce(ct).ConfigureAwait(false);
            }
            catch (Exception ex) when (ex is not OperationCanceledException)
            {
                _log.LogWarning("housekeeping failed: {Error}", ex.Message);
            }
        }
    }

    internal async Task HousekeepingOnce(CancellationToken ct)
    {
        var ended = State.ExpireSessionIfDue();
        if (ended is not null)
        {
            RaiseEvent(e => e.SessionEnded = new SessionEnded { SessionId = ended, Reason = SessionEndReason.Expired });
            ScheduleAfterSession();
        }

        if (State.ExitServiceIfDue())
            RaiseEvent(e => e.ServiceModeChanged = new ServiceModeChanged { Active = false });

        if (_afterSessionAt is { } at && _time.GetUtcNow() >= at)
        {
            _afterSessionAt = null;
            if (State.ComputerState is ComputerState.Idle or ComputerState.Locked && _platform.Capabilities.PowerControl)
            {
                State.SetShuttingDown(true);
                await PowerOffAsync(_afterSessionAction == AfterSessionAction.Restart, TimeSpan.Zero, ct).ConfigureAwait(false);
            }
        }

        var idleMinutes = State.Config?.IdleLogoutMinutes ?? 0;
        // Пока ждёт автобиллинг (пауза на выбор тарифа), гостя не выводит (idleLogoutTick старого агента).
        if (idleMinutes > 0 && State.ComputerState == ComputerState.Locked && State.IdleSeconds >= idleMinutes * 60
            && State.AutoBilling?.Waiting != true)
        {
            var user = State.SignOut();
            if (user > 0)
            {
                // Сервер отзывает токен гостя по событию GuestSignedOut (guestapi Hooks.HandleEvent).
                RaiseEvent(e => e.GuestSignedOut = new GuestSignedOut { UserId = user, Reason = SignOutReason.Idle });
                State.SetIdle(0);
            }
        }

        await MaybeRenewAsync(ct).ConfigureAwait(false);
    }

    private async Task MaybeRenewAsync(CancellationToken ct)
    {
        AgentCredentials? creds;
        AgentChannel? channel;
        lock (_gate)
        {
            creds = _creds;
            channel = _channel;
            if (creds is null || channel is null || _renewing) return;
            if (_renewAt is null)
            {
                var renewAfter = State.CertificateRenewAfterOverride is { } o && o < creds.RenewAfter ? o : creds.RenewAfter;
                _renewAt = Backoff.RenewAt(renewAfter, creds.NotAfter, _random);
            }

            if (_time.GetUtcNow() < _renewAt) return;
            _renewing = true;
        }

        try
        {
            var renewed = await _enroller.RenewAsync(channel, creds, _device, ct).ConfigureAwait(false);
            ReplaceCredentials(renewed);
            // Новый канал с новым сертификатом: текущий стрим закрывается, цикл переподключается сразу.
            await ReconnectAfterAsync(TimeSpan.Zero).ConfigureAwait(false);
        }
        catch (AgentRpcException e)
        {
            _log.LogWarning("certificate renewal failed: {Error}", e.Message);
            lock (_gate) _renewAt = _time.GetUtcNow() + Backoff.Next(6, _random, e.RetryAfter) + TimeSpan.FromMinutes(5);
        }
        finally
        {
            lock (_gate) _renewing = false;
        }
    }

    /// <summary>Отладка и тесты: принудительно продлить сертификат при следующей проверке.</summary>
    public void RenewSoon()
    {
        lock (_gate) _renewAt = _time.GetUtcNow();
    }

    public ValueTask DisposeAsync()
    {
        State.Changed -= OnStateChanged;
        _health.Urgent -= OnHealthUrgent;
        // Стрим ещё открыт (освобождение без остановки RunAsync): закрыть его и канал gRPC. Обычно оба уже сняты —
        // RunStreamAsync освобождает их в finally (канал — Dispose, streamCts — using), поэтому CTS здесь только
        // отменяется: его освободит владелец, а повторный Dispose канала безопасен.
        AgentChannel? channel;
        CancellationTokenSource? streamCts;
        lock (_gate)
        {
            channel = _channel;
            streamCts = _streamCts;
            _channel = null;
            _streamCts = null;
        }

        try
        {
            streamCts?.Cancel();
        }
        catch (ObjectDisposedException)
        {
        }

        channel?.Dispose();
        Credentials?.Dispose();
        _wake.Dispose();
        _enrollGate.Dispose();
        return ValueTask.CompletedTask;
    }
}
