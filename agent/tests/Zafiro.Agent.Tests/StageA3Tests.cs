using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Grpc.Core;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Guest;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.UI.Bridge;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>
/// Пробелы протокола A3: автобиллинг и «на весь баланс» на сервере (AG-12), картинки клуба через службу (AG-13), язык
/// текстов сервера (AG-16), тариф и версия в снимке (AG-17).
/// </summary>
public sealed class StageA3Tests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private static GuestSignIn SignIn(long user) => new()
    {
        Token = new GuestToken { AccessToken = "secret-token" },
        User = new GuestUser { UserId = user, Name = "Гость" },
        Balance = new Balance { MainTiyn = 140000, Currency = "KZT" },
    };

    private static Command Cmd(Action<Command> kind)
    {
        var c = new Command { CommandId = Guid.CreateVersion7().ToString(), IssuedAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow) };
        kind(c);
        return c;
    }

    [Fact]
    public async Task AG12_AutoBillingChanged_в_снимке_и_гость_не_выходит_по_бездействию_пока_ждёт()
    {
        await using var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        rt.State.SetConnection(Connection.Online);
        rt.State.ApplyConfig(new ClubConfig { IdleLogoutMinutes = 1, DefaultLocale = "ru" });
        rt.State.SignIn(SignIn(5));
        var pause = DateTimeOffset.UtcNow.AddMinutes(3);
        var waiting = new AutoBillingStatus { Enabled = true, Waiting = true, PauseEndsAt = Timestamp.FromDateTimeOffset(pause) };

        // Чужой гость — игнорируется.
        await rt.HandleCommandAsync(Cmd(c => c.AutoBillingChanged = new AutoBillingChanged { UserId = 9, Status = waiting }), CancellationToken.None);
        Assert.Null(rt.State.BuildSnapshot().AutoBilling);
        var r = await rt.HandleCommandAsync(Cmd(c => c.AutoBillingChanged = new AutoBillingChanged { UserId = 5, Status = waiting }), CancellationToken.None);
        Assert.Equal(CommandStatus.Succeeded, r.Status);
        Assert.True(rt.State.BuildSnapshot().AutoBilling.Waiting);

        // Ждёт автобиллинг — бездействие не выводит гостя (idleLogoutTick старого агента).
        rt.State.SetIdle(600);
        await rt.HousekeepingOnce(CancellationToken.None);
        Assert.Equal(5, rt.State.SignedInUserId);

        // Остановлен — выводит, состояние автобиллинга забыто.
        await rt.HandleCommandAsync(Cmd(c => c.AutoBillingChanged = new AutoBillingChanged { UserId = 5, Status = new AutoBillingStatus { Enabled = true, Stopped = true } }), CancellationToken.None);
        rt.State.SetIdle(600);
        await rt.HousekeepingOnce(CancellationToken.None);
        Assert.Equal(0, rt.State.SignedInUserId);
        Assert.Null(rt.State.BuildSnapshot().AutoBilling);
    }

    [Fact]
    public void AG16_язык_гостя_только_из_языков_клуба_и_сбрасывается_при_выходе()
    {
        var s = new AgentState { AgentVersion = "2.1.0", FallbackLocale = "kk" };
        Assert.Equal("kk", s.RequestLocale); // ещё нет Welcome — язык ПК
        s.ApplyConfig(new ClubConfig { DefaultLocale = "ru", Languages = { new Language { Code = "ru" }, new Language { Code = "en" } } });
        Assert.Equal("ru", s.RequestLocale); // язык клуба
        Assert.False(s.SetGuestLocale("de"));
        Assert.True(s.SetGuestLocale("en"));
        Assert.Equal("en", s.RequestLocale);
        var snap = s.BuildSnapshot();
        Assert.Equal("en", snap.Locale);
        Assert.Equal("2.1.0", snap.AgentVersion); // AG-17
        s.SignIn(SignIn(5));
        s.SignOut();
        Assert.Equal("ru", s.RequestLocale);
        Assert.True(s.SetGuestLocale("en"));
        Assert.True(s.SetGuestLocale(""));
        Assert.Equal("ru", s.RequestLocale);
    }

    [Fact]
    public async Task AG16_SetLocale_через_IPC_и_отказ_чужому_языку()
    {
        await using var rt = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        rt.State.ApplyConfig(new ClubConfig { DefaultLocale = "ru", Languages = { new Language { Code = "ru" }, new Language { Code = "kk" } } });
        var d = new IpcDispatcher(rt);
        var ok = await d.DispatchAsync(new Request { Id = 1, Method = "agent.SetLocale", Payload = new SetLocaleRequest { Locale = "kk" }.ToByteString() }, CancellationToken.None);
        Assert.Null(ok.Error);
        Assert.Equal("kk", Snapshot.Parser.ParseFrom(ok.Payload).Locale);
        var bad = await d.DispatchAsync(new Request { Id = 2, Method = "agent.SetLocale", Payload = new SetLocaleRequest { Locale = "en" }.ToByteString() }, CancellationToken.None);
        Assert.Equal(ErrorCode.ValidationFailed, bad.Error.ErrorCode);
        Assert.Equal("kk", rt.State.RequestLocale);
    }

    [Fact]
    public void AG17_тариф_сессии_в_снимке()
    {
        var s = new AgentState();
        s.StartSession(new ActiveSession { SessionId = "a", UserId = 5, TariffName = "Ночь", EndsAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddHours(1)) });
        Assert.Equal("Ночь", s.BuildSnapshot().Session.TariffName);
    }

    private static AgentRpcException Rpc(StatusCode code, ErrorCode error) =>
        new(code, error, null, "x", null, new Dictionary<string, string>(), [], null);

    private sealed class FakeTime : TimeProvider
    {
        public DateTimeOffset Now { get; set; } = DateTimeOffset.UtcNow;
        public override DateTimeOffset GetUtcNow() => Now;
    }

    [Fact]
    public async Task AG13_кэш_картинок_свежая_etag_без_сети_и_удалённая()
    {
        var time = new FakeTime();
        var png = ByteString.CopyFrom([0x89, 0x50, 0x4E, 0x47]);
        var calls = new List<GetImageRequest>();
        Func<GetImageRequest, GetImageResponse> server = _ => new GetImageResponse { Data = png, ContentType = "image/png", Etag = "e1" };
        var cache = new ImageCache(Path.Combine(_dir.Path, "images"), (r, _) =>
        {
            calls.Add(r.Clone());
            return Task.FromResult(server(r));
        }, time);
        var logo = new GetImageRequest { Kind = ImageKind.ClubLogo };

        Assert.Equal(png, (await cache.GetAsync(logo, CancellationToken.None)).Data);
        Assert.Equal(png, (await cache.GetAsync(logo, CancellationToken.None)).Data);
        Assert.Single(calls); // свежая — без сервера

        time.Now += ImageCache.FreshFor;
        server = r => new GetImageResponse { NotModified = r.IfNoneMatch == "e1", ContentType = "image/png", Etag = "e1" };
        Assert.Equal(png, (await cache.GetAsync(logo, CancellationToken.None)).Data);
        Assert.Equal("e1", calls[^1].IfNoneMatch);

        // Сервер недоступен — из кэша на диске (и после перезапуска службы).
        time.Now += ImageCache.FreshFor;
        var offline = new ImageCache(Path.Combine(_dir.Path, "images"), (_, _) => throw Rpc(StatusCode.Unavailable, ErrorCode.Unspecified), time);
        Assert.Equal(png, (await offline.GetAsync(logo, CancellationToken.None)).Data);

        // Картинку удалили в клубе — NOT_FOUND, из кэша тоже.
        var gone = new ImageCache(Path.Combine(_dir.Path, "images"), (_, _) => throw Rpc(StatusCode.NotFound, ErrorCode.ValidationFailed), time);
        await Assert.ThrowsAsync<AgentRpcException>(() => gone.GetAsync(logo, CancellationToken.None));
        await Assert.ThrowsAsync<AgentRpcException>(() => offline.GetAsync(logo, CancellationToken.None));

        // Не PNG/JPEG или больше 3 МиБ — не принимается.
        server = _ => new GetImageResponse { Data = ByteString.CopyFrom(new byte[ImageCache.MaxImageBytes + 1]), ContentType = "image/png", Etag = "big" };
        await Assert.ThrowsAsync<AgentRpcException>(() => cache.GetAsync(new GetImageRequest { Kind = ImageKind.Signin }, CancellationToken.None));
        server = _ => new GetImageResponse { Data = png, ContentType = "image/svg+xml", Etag = "svg" };
        await Assert.ThrowsAsync<AgentRpcException>(() => cache.GetAsync(new GetImageRequest { Kind = ImageKind.News, Id = 3 }, CancellationToken.None));

        Assert.Throws<ArgumentException>(() => ImageCache.Key(new GetImageRequest { Kind = ImageKind.News }));
        Assert.Throws<ArgumentException>(() => ImageCache.Key(new GetImageRequest { Kind = ImageKind.ClubLogo, Id = 7 }));
        Assert.Throws<ArgumentException>(() => ImageCache.Key(new GetImageRequest()));
        Assert.Equal("news-12", ImageCache.Key(new GetImageRequest { Kind = ImageKind.News, Id = 12 }));
    }

    [Theory]
    [InlineData("https://img.zafiro.invalid/logo", ImageKind.ClubLogo, 0L)]
    [InlineData("https://img.zafiro.invalid/signin?v=abc.png", ImageKind.Signin, 0L)]
    [InlineData("https://img.zafiro.invalid/news/42", ImageKind.News, 42L)]
    public void AG13_адрес_картинки_разбирается(string uri, ImageKind kind, long id)
    {
        var r = BridgeRouter.ParseImageUri(uri);
        Assert.NotNull(r);
        Assert.Equal(kind, r!.Kind);
        Assert.Equal(id, r.Id);
    }

    [Theory]
    [InlineData("https://img.zafiro.invalid/news/0")]
    [InlineData("https://img.zafiro.invalid/news/-1")]
    [InlineData("https://img.zafiro.invalid/news/1e3")]
    [InlineData("https://img.zafiro.invalid/../logo")]
    [InlineData("https://img.zafiro.invalid/")]
    [InlineData("https://app.zafiro.invalid/logo")]
    [InlineData("https://evil.example/news/1")]
    [InlineData(null)]
    public void AG13_чужой_адрес_не_картинка_клуба(string? uri) => Assert.Null(BridgeRouter.ParseImageUri(uri));

    [Fact]
    public async Task AG13_страница_не_вызывает_GetImage_мостом()
    {
        var called = false;
        var router = new BridgeRouter(new CallbackTransport(() => called = true), new NoHost());
        var reply = await router.HandleAsync("""{"id":1,"method":"agent.GetImage","params":{"kind":"IMAGE_KIND_CLUB_LOGO"}}""", CancellationToken.None);
        Assert.Contains("\"error\"", reply, StringComparison.Ordinal);
        Assert.False(called);
        Assert.True(IpcMethods.All.ContainsKey("agent.GetImage"));
        Assert.True(IpcMethods.All["GuestService/PlayWholeBalance"].HasIdempotencyKey);
    }

    private sealed class CallbackTransport(Action onCall) : Zafiro.Agent.UI.Ipc.IIpcTransport
    {
        public bool IsConnected => true;
#pragma warning disable CS0067
        public event Action<Event>? EventReceived;
        public event Action<bool>? ConnectionChanged;
#pragma warning restore CS0067

        public Task<Response> CallAsync(string method, ByteString payload, CancellationToken cancellationToken, TimeSpan? timeout = null)
        {
            onCall();
            return Task.FromResult(new Response());
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
