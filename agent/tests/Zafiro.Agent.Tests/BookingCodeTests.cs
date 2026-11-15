using System.Text.Json;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Google.Rpc;
using Grpc.Core;
using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Guest;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.UI.Bridge;
using Zafiro.Agent.UI.Screens;
using Zafiro.Agent.V1;
using RpcStatus = Google.Rpc.Status;

namespace Zafiro.Agent.Tests;

/// <summary>
/// Вход по коду брони из мобильного приложения (тема Ж; старый агент — ClubSetupWindow.EnterBookingCodeAsync): служба
/// зовёт GuestAuthService.StartBookingByCode и сразу применяет сессию без аккаунта; UI ходит только через IPC; ошибки
/// сервера — типизированные коды с текстом для гостя; ПК с такой сессией открыт без лаунчера (нет гостя — нет магазина,
/// баланса и профиля), по окончании — снова экран входа.
/// </summary>
public sealed class BookingCodeTests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private static ActiveSession GuestSession(string id = "booking-1") => new()
    {
        SessionId = id, Guest = true, EndsAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddMinutes(50)),
    };

    [Fact]
    public async Task Служба_применяет_сессию_без_аккаунта_из_ответа()
    {
        var state = new AgentState();
        state.SetConnection(Connection.Online);
        var invoker = new FakeInvoker { Reply = _ => new StartBookingByCodeResponse { Session = GuestSession() } };
        var events = new RecordingEvents();
        var api = new GuestApi(() => invoker, state, new AgentOptions(), TimeProvider.System, events);

        var resp = await api.StartBookingByCodeAsync(new StartBookingByCodeRequest { Code = "123456" });

        Assert.Equal("booking-1", resp.Session.SessionId);
        Assert.Equal("/zafiro.agent.v1.GuestAuthService/StartBookingByCode", Assert.Single(invoker.Calls).Method);
        Assert.Equal("123456", ((StartBookingByCodeRequest)invoker.Calls[0].Request).Code);
        // Токена гостя нет и не появляется: вход по коду — не вход в аккаунт.
        Assert.DoesNotContain(invoker.Calls[0].Headers, h => h.Key == "authorization");
        Assert.Null(state.GuestToken);
        Assert.Equal(0, events.SignedInCount);
        // ПК открыт: идёт сессия, экран не нужен; в снимке — сессия без гостя.
        Assert.Equal(ComputerState.InSession, state.ComputerState);
        Assert.Equal(Screen.None, state.Screen);
        var snap = state.BuildSnapshot();
        Assert.True(snap.Session.Guest);
        Assert.Null(snap.Guest);
        Assert.Null(snap.Balance);
        Assert.Equal(0, state.BuildStatus(new ComputerLoad()).SignedInUserId);
        // Та же сессия командой StartSession сервера (ответ мог не дойти) — ничего не меняет.
        state.StartSession(GuestSession());
        Assert.Equal("booking-1", state.BuildSnapshot().Session.SessionId);
    }

    [Theory]
    [InlineData(Code.NotFound, "ERROR_CODE_BOOKING_CODE_NOT_FOUND", ErrorCode.BookingCodeNotFound, "Код брони не найден или бронь уже закончилась.")]
    [InlineData(Code.FailedPrecondition, "ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER", ErrorCode.BookingCodeOtherComputer, "Этот код брони не для этого ПК: сядьте за забронированный ПК.")]
    [InlineData(Code.FailedPrecondition, "ERROR_CODE_BOOKING_NOT_STARTED", ErrorCode.BookingNotStarted, "Бронь начнётся в 16.11 18:00. Код заработает за 30 мин до начала.")]
    [InlineData(Code.FailedPrecondition, "ERROR_CODE_COMPUTER_BUSY", ErrorCode.ComputerBusy, "На этом ПК уже идёт сессия.")]
    [InlineData(Code.ResourceExhausted, "ERROR_CODE_RATE_LIMITED", ErrorCode.RateLimited, "Слишком много попыток. Повторите позже.")]
    public async Task Отказ_сервера_типизирован_и_сессию_не_открывает(Code code, string reason, ErrorCode expected, string text)
    {
        var state = new AgentState();
        state.SetConnection(Connection.Online);
        var invoker = new FakeInvoker { Reply = _ => throw Rpc(code, reason, text) };
        var api = new GuestApi(() => invoker, state, new AgentOptions(), TimeProvider.System, new RecordingEvents());

        var e = await Assert.ThrowsAnyAsync<AgentRpcException>(() => api.StartBookingByCodeAsync(new StartBookingByCodeRequest { Code = "123456" }));

        Assert.Equal(expected, e.ErrorCode);
        Assert.Equal(text, e.LocalizedMessage);
        // Один вызов: служба сама код не повторяет (лимит — 5 попыток в минуту на ПК).
        Assert.Single(invoker.Calls);
        Assert.Equal(ComputerState.Idle, state.ComputerState);
        Assert.Equal(Screen.Lock, state.Screen);
        // Для UI — код, текст и пауза лимита (IpcDispatcher.ToError).
        var error = IpcDispatcher.ToError(e);
        Assert.Equal(expected, error.ErrorCode);
        Assert.Equal(text, error.Message);
        Assert.Equal(expected == ErrorCode.RateLimited, error.RetryAfterMs > 0);
    }

    [Fact]
    public async Task IPC_без_связи_с_сервером_отвечает_ошибкой_а_не_падает()
    {
        await using var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        var d = new IpcDispatcher(rt);
        Assert.Contains("GuestAuthService/StartBookingByCode", d.Methods);
        var reply = await d.DispatchAsync(new Request
        {
            Id = 1, Method = "GuestAuthService/StartBookingByCode", Payload = new StartBookingByCodeRequest { Code = "123456" }.ToByteString(),
        }, CancellationToken.None);
        Assert.Equal((int)StatusCode.Unavailable, reply.Error.GrpcStatus);
        Assert.Null(rt.State.Session);
    }

    [Fact]
    public async Task Мост_страницы_принимает_только_код()
    {
        var ipc = new FakeTransport
        {
            Reply = _ => new Response { Payload = new StartBookingByCodeResponse { Session = GuestSession() }.ToByteString() },
        };
        var router = new BridgeRouter(ipc, new NoHost());

        var ok = Parse(await router.HandleAsync("""{"id":1,"method":"GuestAuthService/StartBookingByCode","params":{"code":"123456"}}""", CancellationToken.None));
        Assert.True(ok.GetProperty("result").GetProperty("session").GetProperty("guest").GetBoolean());
        Assert.Equal("123456", ((StartBookingByCodeRequest)Assert.Single(ipc.Calls).Request).Code);
        Assert.Null(BridgeRouter.TimeoutOf("GuestAuthService/StartBookingByCode"));

        // Лишнее поле и не строка — отказ без вызова службы.
        var extra = Parse(await router.HandleAsync("""{"id":2,"method":"GuestAuthService/StartBookingByCode","params":{"code":"123456","computerId":"7"}}""", CancellationToken.None));
        Assert.Equal("ERROR_CODE_VALIDATION_FAILED", extra.GetProperty("error").GetProperty("errorCode").GetString());
        var wrong = Parse(await router.HandleAsync("""{"id":3,"method":"GuestAuthService/StartBookingByCode","params":{"code":{"a":1}}}""", CancellationToken.None));
        Assert.Equal("ERROR_CODE_VALIDATION_FAILED", wrong.GetProperty("error").GetProperty("errorCode").GetString());
        Assert.Single(ipc.Calls);

        // Отказ сервера доходит до страницы с кодом и текстом.
        ipc.Reply = _ => new Response
        {
            Error = new Error { ErrorCode = ErrorCode.BookingCodeOtherComputer, GrpcStatus = (int)StatusCode.FailedPrecondition, Message = "Этот код брони не для этого ПК: сядьте за забронированный ПК." },
        };
        var refused = Parse(await router.HandleAsync("""{"id":4,"method":"GuestAuthService/StartBookingByCode","params":{"code":"654321"}}""", CancellationToken.None)).GetProperty("error");
        Assert.Equal("ERROR_CODE_BOOKING_CODE_OTHER_COMPUTER", refused.GetProperty("errorCode").GetString());
        Assert.Equal("Этот код брони не для этого ПК: сядьте за забронированный ПК.", refused.GetProperty("message").GetString());
    }

    [Fact]
    public void Экран_при_сессии_без_аккаунта_ПК_открыт_без_лаунчера_потом_снова_вход()
    {
        var ui = new UiController();
        ui.OnConnection(true);
        ui.OnSnapshot(new Snapshot { Screen = Screen.Lock, State = ComputerState.Idle });
        Assert.Equal(UiView.Lock, ui.View);
        Assert.True(ui.Kiosk);

        // Код принят: сессия без гостя — окно скрыто (как время с кассы), лаунчер сам не открывается.
        ui.OnSnapshot(new Snapshot { Screen = Screen.None, State = ComputerState.InSession, Session = GuestSession() });
        Assert.Equal(UiView.Hidden, ui.View);
        Assert.False(ui.Kiosk);
        Assert.True(ui.ShouldExit(false));

        // Время брони вышло — экран входа.
        ui.OnSnapshot(new Snapshot { Screen = Screen.Lock, State = ComputerState.Idle });
        Assert.Equal(UiView.Lock, ui.View);
        Assert.True(ui.Kiosk);
    }

    [Fact]
    public void Сессия_без_аккаунта_истекает_по_часам_сервера()
    {
        var time = new FakeTime();
        var state = new AgentState(time);
        state.SetConnection(Connection.Online);
        state.StartSession(new ActiveSession { SessionId = "booking-1", Guest = true, EndsAt = Timestamp.FromDateTimeOffset(time.Now.AddMinutes(50)) });
        Assert.Equal(Screen.None, state.Screen);
        time.Now = time.Now.AddMinutes(51);
        Assert.Equal("booking-1", state.ExpireSessionIfDue());
        Assert.Equal(ComputerState.Idle, state.ComputerState);
        Assert.Equal(Screen.Lock, state.Screen);
    }

    private static JsonElement Parse(string? json)
    {
        Assert.NotNull(json);
        using var doc = JsonDocument.Parse(json);
        return doc.RootElement.Clone();
    }

    private static RpcException Rpc(Code code, string reason, string text)
    {
        var st = new RpcStatus { Code = (int)code, Message = text };
        var info = new ErrorInfo { Reason = reason, Domain = AgentRpcException.Domain };
        if (code == Code.ResourceExhausted) info.Metadata.Add("retry_after_seconds", "42");
        else info.Metadata.Add("field", "code");
        st.Details.Add(Any.Pack(info));
        st.Details.Add(Any.Pack(new LocalizedMessage { Locale = "ru", Message = text }));
        return st.ToRpcException();
    }

    private sealed class FakeTime : TimeProvider
    {
        public DateTimeOffset Now { get; set; } = new(2026, 11, 16, 12, 0, 0, TimeSpan.Zero);

        public override DateTimeOffset GetUtcNow() => Now;
    }

    private sealed class RecordingEvents : IGuestEvents
    {
        public int SignedInCount { get; private set; }

        public void SignedIn(GuestSignIn signIn, SignInMethod method) => SignedInCount++;

        public void SignedOut(long userId, SignOutReason reason)
        {
        }
    }

    /// <summary>Канал gRPC без сети: unary-вызовы отвечает <see cref="Reply"/> (или бросает RpcException).</summary>
    private sealed class FakeInvoker : CallInvoker
    {
        public List<(string Method, IMessage Request, Metadata Headers)> Calls { get; } = [];

        public Func<IMessage, IMessage> Reply { get; set; } = r => r;

        public override AsyncUnaryCall<TResponse> AsyncUnaryCall<TRequest, TResponse>(Method<TRequest, TResponse> method, string? host, CallOptions options, TRequest request)
        {
            Calls.Add((method.FullName, (IMessage)request, options.Headers ?? new Metadata()));
            Task<TResponse> response;
            try
            {
                response = Task.FromResult((TResponse)Reply((IMessage)request));
            }
            catch (RpcException e)
            {
                response = Task.FromException<TResponse>(e);
            }

            return new AsyncUnaryCall<TResponse>(response, Task.FromResult(new Metadata()), () => Grpc.Core.Status.DefaultSuccess, () => new Metadata(), () => { });
        }

        public override TResponse BlockingUnaryCall<TRequest, TResponse>(Method<TRequest, TResponse> method, string? host, CallOptions options, TRequest request) =>
            throw new NotSupportedException();

        public override AsyncServerStreamingCall<TResponse> AsyncServerStreamingCall<TRequest, TResponse>(Method<TRequest, TResponse> method, string? host, CallOptions options, TRequest request) =>
            throw new NotSupportedException();

        public override AsyncClientStreamingCall<TRequest, TResponse> AsyncClientStreamingCall<TRequest, TResponse>(Method<TRequest, TResponse> method, string? host, CallOptions options) =>
            throw new NotSupportedException();

        public override AsyncDuplexStreamingCall<TRequest, TResponse> AsyncDuplexStreamingCall<TRequest, TResponse>(Method<TRequest, TResponse> method, string? host, CallOptions options) =>
            throw new NotSupportedException();
    }

    private sealed class FakeTransport : Zafiro.Agent.UI.Ipc.IIpcTransport
    {
        public List<(string Method, IMessage Request)> Calls { get; } = [];

        public Func<ByteString, Response> Reply { get; set; } = _ => new Response();

        public bool IsConnected => true;
#pragma warning disable CS0067
        public event Action<Event>? EventReceived;
        public event Action<bool>? ConnectionChanged;
#pragma warning restore CS0067

        public Task<Response> CallAsync(string method, ByteString payload, CancellationToken cancellationToken, TimeSpan? timeout = null)
        {
            Calls.Add((method, IpcMethods.All[method].Request.Parser.ParseFrom(payload)));
            return Task.FromResult(Reply(payload));
        }
    }

    private sealed class NoHost : IUiHost
    {
        public void ReturnToDesktop()
        {
        }

        public bool Power(bool restart) => false;

        public string InputLanguage(bool next) => "";
    }
}
