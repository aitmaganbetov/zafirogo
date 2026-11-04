using System.Text.Json;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Service;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Service.Ui;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.UI.Bridge;
using IpcClient = Zafiro.Agent.UI.Ipc.IpcClient;
using Zafiro.Agent.UI.Ipc;
using Zafiro.Agent.UI.Kiosk;
using Zafiro.Agent.UI.Screens;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>
/// Процесс UI без WPF и WebView2: мост страницы (схемы, отказ неизвестному), клиент IPC против настоящего сервера IPC
/// службы (unix socket), выбор экрана, правила киоска, уведомления службы для UI, ключ образа (AG-10).
/// </summary>
public sealed class UiTests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    private sealed class FakeTransport : IIpcTransport
    {
        public List<(string Method, IMessage? Request, TimeSpan? Timeout)> Calls { get; } = [];
        public Func<string, ByteString, Response> Reply { get; set; } = (_, _) => new Response();
        public bool IsConnected => true;
        public event Action<Event>? EventReceived;
        public event Action<bool>? ConnectionChanged;

        public Task<Response> CallAsync(string method, ByteString payload, CancellationToken cancellationToken, TimeSpan? timeout = null)
        {
            var descriptor = IpcMethods.All[method].Request;
            Calls.Add((method, descriptor.Parser.ParseFrom(payload), timeout));
            return Task.FromResult(Reply(method, payload));
        }

        public void Raise(Event e) => EventReceived?.Invoke(e);

        public void Connection(bool connected) => ConnectionChanged?.Invoke(connected);
    }

    private sealed class FakeHost : IUiHost
    {
        public int Returns { get; private set; }
        public List<bool> Power { get; } = [];
        public void ReturnToDesktop() => Returns++;
        bool IUiHost.Power(bool restart)
        {
            Power.Add(restart);
            return true;
        }

        public string InputLanguage(bool next) => next ? "ENG" : "РУС";
    }

    private static JsonElement Parse(string? json)
    {
        Assert.NotNull(json);
        return JsonDocument.Parse(json!).RootElement.Clone();
    }

    [Fact]
    public async Task Мост_вызывает_службу_по_схеме_proto_и_отдаёт_JSON()
    {
        var ipc = new FakeTransport
        {
            Reply = (method, _) => new Response
            {
                Payload = new PurchaseTariffResponse
                {
                    Purchase = new TimePurchase { Message = "Тариф подключён.", Balance = new Balance { MainTiyn = 150050, BonusTiyn = 0, Currency = "KZT" } },
                }.ToByteString(),
            },
        };
        var router = new BridgeRouter(ipc, new FakeHost());
        var reply = Parse(await router.HandleAsync(
            """{"id": 7, "method": "GuestService/PurchaseTariff", "params": {"tariffId": "5", "balanceSource": "BALANCE_SOURCE_BONUS"}}""", CancellationToken.None));

        Assert.Equal(7, reply.GetProperty("id").GetInt64());
        var purchase = reply.GetProperty("result").GetProperty("purchase");
        // int64 (тиыны) — строкой с целым числом, без float.
        Assert.Equal("150050", purchase.GetProperty("balance").GetProperty("mainTiyn").GetString());
        Assert.Equal("0", purchase.GetProperty("balance").GetProperty("bonusTiyn").GetString());
        var (method, request, _) = Assert.Single(ipc.Calls);
        Assert.Equal("GuestService/PurchaseTariff", method);
        var r = Assert.IsType<PurchaseTariffRequest>(request);
        Assert.Equal(5, r.TariffId);
        Assert.Equal(BalanceSource.Bonus, r.BalanceSource);
        Assert.Equal("", r.IdempotencyKey); // ключ заполняет служба (UUID v7, повторы — тем же ключом)
    }

    [Fact]
    public async Task Мост_передаёт_ошибку_сервера_с_текстом()
    {
        var ipc = new FakeTransport
        {
            Reply = (_, _) => new Response
            {
                Error = new Error { ErrorCode = ErrorCode.InsufficientFunds, GrpcStatus = 9, Message = "Недостаточно средств на балансе.", RetryAfterMs = 0 },
            },
        };
        var reply = Parse(await new BridgeRouter(ipc, new FakeHost()).HandleAsync("""{"id":1,"method":"GuestService/PurchaseMinutes","params":{"minutes":30}}""", CancellationToken.None));
        var error = reply.GetProperty("error");
        Assert.Equal("ERROR_CODE_INSUFFICIENT_FUNDS", error.GetProperty("errorCode").GetString());
        Assert.Equal("Недостаточно средств на балансе.", error.GetProperty("message").GetString());
        Assert.Equal(9, error.GetProperty("grpcStatus").GetInt32());
    }

    [Theory]
    [InlineData("""{"id":1,"method":"GuestService/PurchaseTariff","params":{"tariffId":"5","priceTiyn":"1"}}""")] // лишнее поле
    [InlineData("""{"id":1,"method":"GuestService/PurchaseTariff","params":{"tariffId":"x"}}""")] // неверный тип
    [InlineData("""{"id":1,"method":"GuestService/PurchaseTariff","params":{"balanceSource":"BALANCE_SOURCE_GIFT"}}""")] // неизвестный enum
    [InlineData("""{"id":1,"method":"ui.Power","params":{"action":"format c:"}}""")]
    [InlineData("""{"id":1,"method":"ui.QrImage","params":{"text":"http://evil.example/x"}}""")] // только https
    [InlineData("""{"id":1,"method":"ui.ReturnToDesktop","params":{"x":1}}""")]
    public async Task Мост_отклоняет_запрос_не_по_схеме_без_вызова_службы(string json)
    {
        var ipc = new FakeTransport();
        var host = new FakeHost();
        var reply = Parse(await new BridgeRouter(ipc, host).HandleAsync(json, CancellationToken.None));
        Assert.Equal("ERROR_CODE_VALIDATION_FAILED", reply.GetProperty("error").GetProperty("errorCode").GetString());
        Assert.Empty(ipc.Calls);
        Assert.Empty(host.Power);
    }

    [Theory]
    [InlineData("""{"id":1,"method":"AgentService/Connect","params":{}}""")] // не метод IPC
    [InlineData("""{"id":1,"method":"EnrollmentService/Enroll","params":{}}""")]
    [InlineData("""{"id":1,"method":"agent.Shutdown","params":{}}""")]
    public async Task Мост_не_знает_методов_вне_таблицы_IPC(string json)
    {
        var ipc = new FakeTransport();
        var reply = Parse(await new BridgeRouter(ipc, new FakeHost()).HandleAsync(json, CancellationToken.None));
        Assert.Equal((int)Grpc.Core.StatusCode.Unimplemented, reply.GetProperty("error").GetProperty("grpcStatus").GetInt32());
        Assert.Empty(ipc.Calls);
    }

    [Theory]
    [InlineData("")]
    [InlineData("[]")]
    [InlineData("\"x\"")]
    [InlineData("""{"method":"agent.GetSnapshot"}""")] // нет id
    [InlineData("""{"id":0,"method":"agent.GetSnapshot"}""")]
    [InlineData("""{"id":1.5,"method":"agent.GetSnapshot"}""")]
    [InlineData("""{"id":9007199254740992,"method":"agent.GetSnapshot"}""")]
    [InlineData("""{"id":"1","method":"agent.GetSnapshot"}""")]
    [InlineData("""{"id":1,"method":"agent.GetSnapshot","extra":true}""")]
    [InlineData("""{"id":1,"method":"../../x"}""")]
    [InlineData("""{"id":1,"method":"agent.GetSnapshot","params":[]}""")]
    public async Task Мост_отбрасывает_испорченные_сообщения(string json)
    {
        var ipc = new FakeTransport();
        Assert.Null(await new BridgeRouter(ipc, new FakeHost()).HandleAsync(json, CancellationToken.None));
        Assert.Empty(ipc.Calls);
    }

    [Fact]
    public async Task Мост_отбрасывает_слишком_большое_сообщение()
    {
        var ipc = new FakeTransport();
        var big = "{\"id\":1,\"method\":\"SocialService/SendChatMessage\",\"params\":{\"body\":\"" + new string('a', BridgeRouter.MaxMessageBytes) + "\"}}";
        Assert.Null(await new BridgeRouter(ipc, new FakeHost()).HandleAsync(big, CancellationToken.None));
        Assert.Empty(ipc.Calls);
    }

    [Fact]
    public async Task Мост_локальные_методы_и_долгое_ожидание_QR()
    {
        var ipc = new FakeTransport { Reply = (_, _) => new Response { Payload = new WatchQrLoginResponse { Status = QrLoginStatus.Expired }.ToByteString() } };
        var host = new FakeHost();
        var router = new BridgeRouter(ipc, host);

        Assert.Equal("{}", Parse(await router.HandleAsync("""{"id":1,"method":"ui.ReturnToDesktop"}""", CancellationToken.None)).GetProperty("result").GetRawText());
        Assert.Equal(1, host.Returns);
        Assert.True(Parse(await router.HandleAsync("""{"id":2,"method":"ui.Power","params":{"action":"restart"}}""", CancellationToken.None)).GetProperty("result").GetProperty("ok").GetBoolean());
        Assert.Equal([true], host.Power);
        Assert.Equal("ENG", Parse(await router.HandleAsync("""{"id":3,"method":"ui.InputLanguage","params":{"next":true}}""", CancellationToken.None)).GetProperty("result").GetProperty("label").GetString());
        var qr = Parse(await router.HandleAsync("""{"id":4,"method":"ui.QrImage","params":{"text":"https://zafiro.kz/pc-login/abc"}}""", CancellationToken.None));
        Assert.StartsWith("data:image/png;base64,", qr.GetProperty("result").GetProperty("dataUrl").GetString(), StringComparison.Ordinal);

        var watch = Parse(await router.HandleAsync("""{"id":5,"method":"GuestAuthService/WatchQrLogin","params":{"loginId":"0192"}}""", CancellationToken.None));
        Assert.Equal("QR_LOGIN_STATUS_EXPIRED", watch.GetProperty("result").GetProperty("status").GetString());
        Assert.Equal(TimeSpan.FromMinutes(11), Assert.Single(ipc.Calls).Timeout);
    }

    [Fact]
    public void События_службы_в_JSON_для_страницы()
    {
        var json = BridgeRouter.EventJson(new Event
        {
            Snapshot = new Snapshot { Screen = Screen.Lock, Balance = new Balance { MainTiyn = 100 }, Guest = new GuestUser { Name = "Арман" } },
        });
        var snapshot = Parse(json).GetProperty("event").GetProperty("snapshot");
        Assert.Equal("SCREEN_LOCK", snapshot.GetProperty("screen").GetString());
        Assert.Equal("100", snapshot.GetProperty("balance").GetProperty("mainTiyn").GetString());
        Assert.Equal("Арман", snapshot.GetProperty("guest").GetProperty("name").GetString());
    }

    [Fact]
    public void Таблица_IPC_совпадает_с_методами_службы()
    {
        using var dir = new TempDir();
        var runtime = Runtimes.Create(dir.Path, new FakePlatform(), _pki);
        var service = new IpcDispatcher(runtime).Methods.OrderBy(m => m, StringComparer.Ordinal);
        Assert.Equal(service, IpcMethods.All.Keys.OrderBy(m => m, StringComparer.Ordinal));
        Assert.True(IpcMethods.All["GuestService/PurchaseTariff"].HasIdempotencyKey);
        Assert.False(IpcMethods.All["GuestService/ListTariffs"].HasIdempotencyKey);
    }

    [Fact]
    public async Task Клиент_IPC_UI_через_сервер_службы_снимок_вызовы_уведомления()
    {
        if (OperatingSystem.IsWindows()) return;
        await using var runtime = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        var socket = Path.Combine(_dir.Path, "ui.sock");
        var server = new IpcServer(new UnixSocketListener(socket, [], NullLogger<UnixSocketListener>.Instance),
            new IpcDispatcher(runtime), runtime, NullLogger<IpcServer>.Instance);
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(20));
        await server.StartAsync(cts.Token);
        try
        {
            // UI не подключён: уведомление ждёт его (координатор запустит UI ради него).
            server.Publish(new Event { SessionEnding = new SessionEnding { MinutesLeft = 5 } });
            server.Publish(new Event { GuestCounters = new GuestCountersChanged() }); // не уведомление — не копится
            Assert.True(server.HasPendingNotices);

            var events = System.Threading.Channels.Channel.CreateUnbounded<Event>();
            await using var client = new IpcClient(IpcClient.UnixSocket(socket));
            client.EventReceived += e => events.Writer.TryWrite(e);
            var connected = new TaskCompletionSource();
            client.ConnectionChanged += c =>
            {
                if (c) connected.TrySetResult();
            };
            _ = client.RunAsync(cts.Token);
            await connected.Task.WaitAsync(cts.Token);

            Assert.Equal(Screen.Setup, (await events.Reader.ReadAsync(cts.Token)).Snapshot.Screen);
            Assert.Equal(5, (await events.Reader.ReadAsync(cts.Token)).SessionEnding.MinutesLeft);
            Assert.False(server.HasPendingNotices);

            // Мост → клиент → служба: снимок и отказ гостю без входа (токена нет — GUEST_TOKEN_INVALID без сервера).
            var router = new BridgeRouter(client, new FakeHost());
            var snap = Parse(await router.HandleAsync("""{"id":1,"method":"agent.GetSnapshot"}""", cts.Token));
            Assert.Equal("CONNECTION_NOT_ENROLLED", snap.GetProperty("result").GetProperty("connection").GetString());
            var tariffs = Parse(await router.HandleAsync("""{"id":2,"method":"GuestService/ListTariffs"}""", cts.Token));
            Assert.Equal("ERROR_CODE_GUEST_TOKEN_INVALID", tariffs.GetProperty("error").GetProperty("errorCode").GetString());

            // UI подключён: уведомление — сразу.
            server.Publish(new Event { SessionEnding = new SessionEnding { MinutesLeft = 1 } });
            Event next;
            do next = await events.Reader.ReadAsync(cts.Token);
            while (next.KindCase != Event.KindOneofCase.SessionEnding);
            Assert.Equal(1, next.SessionEnding.MinutesLeft);
            Assert.False(server.HasPendingNotices);
        }
        finally
        {
            await server.StopAsync(CancellationToken.None);
        }
    }

    [Fact]
    public async Task Клиент_IPC_без_службы_отвечает_ошибкой_мосту()
    {
        await using var client = new IpcClient(_ => throw new IOException("no service"));
        var reply = Parse(await new BridgeRouter(client, new FakeHost()).HandleAsync("""{"id":1,"method":"agent.GetSnapshot"}""", CancellationToken.None));
        Assert.Equal((int)Grpc.Core.StatusCode.Unavailable, reply.GetProperty("error").GetProperty("grpcStatus").GetInt32());
        Assert.Equal(TimeSpan.FromMilliseconds(500), IpcClient.ReconnectDelay(0));
        Assert.Equal(TimeSpan.FromSeconds(5), IpcClient.ReconnectDelay(10));
    }

    [Fact]
    public void Уведомления_для_UI_только_важные()
    {
        Assert.True(IpcServer.IsNotice(new Event { ShowMessage = new ShowMessage { Text = "x" } }));
        Assert.True(IpcServer.IsNotice(new Event { SessionEnding = new SessionEnding() }));
        Assert.True(IpcServer.IsNotice(new Event { ChatMessage = new ChatMessageReceived { Message = new ChatMessage { FromStaff = true } } }));
        Assert.False(IpcServer.IsNotice(new Event { ChatMessage = new ChatMessageReceived { Message = new ChatMessage { FromStaff = false } } }));
        Assert.False(IpcServer.IsNotice(new Event { Snapshot = new Snapshot() }));
    }

    [Fact]
    public void Предупреждения_о_конце_сессии_10_5_3_1_по_одному_разу()
    {
        using var dir = new TempDir();
        var runtime = Runtimes.Create(dir.Path, new FakePlatform(), _pki);
        var coordinator = new UiCoordinator(runtime, null!, new NoUiLauncher(), TimeProvider.System, NullLogger<UiCoordinator>.Instance);
        var end = DateTimeOffset.Parse("2026-11-02T12:00:00Z", System.Globalization.CultureInfo.InvariantCulture);
        var warnings = new List<int>();
        // Сессия куплена за 12 минут до конца: сразу после покупки — без уведомления.
        for (var left = TimeSpan.FromMinutes(12); left > TimeSpan.Zero; left -= TimeSpan.FromSeconds(3))
        {
            if (coordinator.CheckSessionEnding(end, end - left) is { } w) warnings.Add(w.MinutesLeft);
        }

        Assert.Equal([10, 5, 3, 1], warnings);
        // Продлили за 4 минуты до нового конца — отсчёт заново, без уведомления сразу (5 уже прошли), затем 3.
        Assert.Null(coordinator.CheckSessionEnding(end.AddMinutes(4), end));
        Assert.Equal(3, coordinator.CheckSessionEnding(end.AddMinutes(4), end.AddMinutes(1).AddSeconds(1))!.MinutesLeft);
        // Сессии нет — ничего.
        Assert.Null(coordinator.CheckSessionEnding(null, end));
    }

    [Fact]
    public void Экран_UI_по_снимку_службы()
    {
        var ui = new UiController();
        var changes = 0;
        ui.Changed += () => changes++;
        ui.OnConnection(true);
        Assert.False(ui.ShouldExit(false)); // снимка ещё нет: нужен ли экран — неизвестно, процесс ждёт
        ui.OnSnapshot(new Snapshot { Screen = Screen.Setup, Connection = Connection.NotEnrolled });
        Assert.Equal(UiView.Setup, ui.View);
        Assert.False(ui.Kiosk);

        ui.OnSnapshot(new Snapshot { Screen = Screen.Lock, State = ComputerState.Idle });
        Assert.Equal(UiView.Lock, ui.View);
        Assert.True(ui.Kiosk);

        // Гость оплатил время на экране блокировки — лаунчер остаётся открытым (без киоска).
        ui.OnSnapshot(new Snapshot { Screen = Screen.None, State = ComputerState.InSession, Guest = new GuestUser { Name = "А" } });
        Assert.Equal(UiView.Launcher, ui.View);
        Assert.False(ui.Kiosk);
        Assert.False(ui.ShouldExit(false));

        // «Вернуться к игре»: окно закрывается, процесс UI выходит.
        ui.ReturnToDesktop();
        Assert.Equal(UiView.Hidden, ui.View);
        Assert.True(ui.ShouldExit(false));
        Assert.False(ui.ShouldExit(true)); // пока видно уведомление

        // Ярлык «Zafiro» во время сессии.
        ui.RequestLauncher();
        Assert.Equal(UiView.Launcher, ui.View);

        // Техрежим (PIN сотрудника) — окно закрывается.
        ui.OnSnapshot(new Snapshot { Screen = Screen.None, State = ComputerState.Service });
        Assert.Equal(UiView.Hidden, ui.View);

        // Обрыв канала к службе (обновление) — экран блокировки остаётся, процесс не выходит.
        ui.OnSnapshot(new Snapshot { Screen = Screen.Lock, State = ComputerState.Locked });
        ui.OnConnection(false);
        Assert.Equal(UiView.Lock, ui.View);
        Assert.False(ui.ShouldExit(false));
        Assert.True(changes >= 6);
    }

    [Fact]
    public void UI_без_службы_выходит_через_15_секунд()
    {
        var time = new ManualTime(DateTimeOffset.Parse("2026-11-02T12:00:00Z", System.Globalization.CultureInfo.InvariantCulture));
        var ui = new UiController(launcherRequested: true, time);
        Assert.False(ui.ShouldExit(false));
        time.Now += UiController.FirstConnectTimeout;
        Assert.True(ui.ShouldExit(false));
    }

    private sealed class ManualTime(DateTimeOffset now) : TimeProvider
    {
        public DateTimeOffset Now { get; set; } = now;
        public override DateTimeOffset GetUtcNow() => Now;
    }

    [Theory]
    [InlineData(new string[0], false)]
    [InlineData(new[] { "--launcher" }, true)]
    [InlineData(new[] { "--screen", "launcher" }, true)]
    [InlineData(new[] { "--screen", "lock" }, false)]
    public void Аргументы_UI(string[] args, bool launcher)
    {
        var o = LaunchOptions.Parse(args);
        Assert.Equal(launcher, o.Launcher);
        Assert.Equal(LaunchOptions.DefaultPipeName, o.PipeName);
        Assert.Equal("Dev.Pipe", LaunchOptions.Parse(["--pipe=Dev.Pipe"]).PipeName);
        Assert.Equal(LaunchOptions.DefaultPipeName, LaunchOptions.Parse(["--pipe=..\\evil"]).PipeName);
    }

    [Theory]
    [InlineData(KioskRules.VkTab, true, false, false, true, false, true)] // Alt+Tab без сессии
    [InlineData(KioskRules.VkTab, true, false, false, false, false, false)] // Alt+Tab во время сессии
    [InlineData(KioskRules.VkEscape, false, true, true, false, false, true)] // Ctrl+Shift+Esc всегда
    [InlineData(KioskRules.VkLWin, false, false, false, true, false, true)]
    [InlineData(KioskRules.VkTab, false, false, false, false, true, true)] // Win+Tab всегда
    [InlineData(KioskRules.VkF4, true, false, false, true, false, true)] // Alt+F4
    [InlineData(0x41, false, false, false, true, false, false)] // буква
    public void Правила_киоска(int vk, bool alt, bool ctrl, bool shift, bool strict, bool win, bool blocked)
    {
        Assert.Equal(blocked, KioskRules.ShouldBlock(vk, alt, ctrl, shift, strict, win));
        Assert.True(KioskRules.IsStaffShortcut(KioskRules.VkP, alt: true, ctrl: true));
        Assert.False(KioskRules.IsStaffShortcut(KioskRules.VkP, alt: false, ctrl: true));
    }

    [Fact]
    public async Task Тексты_уведомлений_из_переводов_службы()
    {
        var ipc = new FakeTransport
        {
            Reply = (_, _) => new Response
            {
                Payload = new GetTranslationsResponse { Locale = "kk", Version = 3, Lines = { ["До конца сессии осталось :count мин."] = "Сессияның аяқталуына :count мин қалды." } }.ToByteString(),
            },
        };
        var texts = new UiTranslations(ipc);
        Assert.Equal("Сообщение администратора", texts.T("Сообщение администратора"));
        await texts.RefreshAsync("kk", 3, CancellationToken.None);
        Assert.Equal("Сессияның аяқталуына 5 мин қалды.", texts.T("До конца сессии осталось :count мин.", ("count", "5")));
        await texts.RefreshAsync(null, 3, CancellationToken.None); // та же версия — без запроса
        Assert.Single(ipc.Calls);
    }

    [Fact]
    public void Ключ_образа_переносится_из_appsettings_в_файл_0600()
    {
        if (OperatingSystem.IsWindows()) return;
        var data = Path.Combine(_dir.Path, "data");
        var settings = Path.Combine(_dir.Path, "appsettings.json");
        File.WriteAllText(settings, """
            {
              // комментарий
              "Agent": { "EnrollEndpoint": "agent.test:443", "ImageKey": "zik_secret123" },
              "Service": { "PipeName": "ZafiroAgent.Ipc" }
            }
            """);

        var (key, result) = ImageKeyStore.Resolve("zik_secret123", data, settings);
        Assert.Equal("zik_secret123", key);
        Assert.Equal(ImageKeyMigration.Migrated, result);
        var file = Path.Combine(data, ImageKeyStore.FileName);
        Assert.Equal(UnixFileMode.UserRead | UnixFileMode.UserWrite, File.GetUnixFileMode(file));
        var json = JsonDocument.Parse(File.ReadAllText(settings)).RootElement;
        Assert.Equal("", json.GetProperty("Agent").GetProperty("ImageKey").GetString());
        Assert.Equal("agent.test:443", json.GetProperty("Agent").GetProperty("EnrollEndpoint").GetString());
        Assert.Equal("ZafiroAgent.Ipc", json.GetProperty("Service").GetProperty("PipeName").GetString());
        Assert.DoesNotContain("zik_secret123", File.ReadAllText(settings), StringComparison.Ordinal);

        // Следующий запуск: в конфигурации пусто — ключ из файла.
        Assert.Equal(("zik_secret123", ImageKeyMigration.Loaded), ImageKeyStore.Resolve("", data, settings));
        // Ключ задан переменной окружения (в файле его нет) — сохраняется, но остаётся в конфигурации.
        Assert.Equal(ImageKeyMigration.MigratedButStillInConfig, ImageKeyStore.Resolve("zik_other", data, settings).Result);
        Assert.Equal("zik_other", ImageKeyStore.Load(data));
        ImageKeyStore.Delete(data);
        Assert.Equal((null, ImageKeyMigration.None), ImageKeyStore.Resolve(null, data, settings));
        Assert.Throws<InvalidDataException>(() => ImageKeyStore.Resolve("bad key with spaces", data, settings));
        Assert.False(ImageKeyStore.IsValid(new string('k', 257)));
    }
}
