using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json.Nodes;
using Xunit.Abstractions;
using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Core.Transport;
using Zafiro.Agent.Core.Updates;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests.Integration;

/// <summary>
/// Сквозной сценарий против настоящего Go-сервера (agent/tests/integration/server.sh: Postgres, Valkey, NATS, dev-PKI,
/// демо-клуб): код регистрации из панели → Enroll → Connect → Welcome → Status → команда с сервера доходит и
/// подтверждается → повторное подключение без повтора команды → вход гостя → тарифы → покупка с повтором ключа
/// идемпотентности (одно списание) → BalanceChanged → завершение сессии → отзыв сертификата закрывает стрим.
/// Запуск: agent/test.sh integration (переменные ZAFIRO_IT_* — из env-файла server.sh).
/// </summary>
[Trait("Category", "Integration")]
public sealed class ServerScenarioTests : IDisposable
{
    private readonly ITestOutputHelper _out;
    private readonly TempDir _dir = new();
    private readonly HttpClient _http;
    private readonly string _rootCa;
    private readonly string _enrollEndpoint;

    public ServerScenarioTests(ITestOutputHelper output)
    {
        _out = output;
        _http = new HttpClient { BaseAddress = new Uri(Env("ZAFIRO_IT_HTTP")), Timeout = TimeSpan.FromSeconds(30) };
        _rootCa = File.ReadAllText(Env("ZAFIRO_IT_ROOT_CA"));
        _enrollEndpoint = Env("ZAFIRO_IT_ENROLL_ENDPOINT");
    }

    public void Dispose()
    {
        _http.Dispose();
        _dir.Dispose();
    }

    private static string Env(string name) =>
        Environment.GetEnvironmentVariable(name) is { Length: > 0 } v
            ? v
            : throw new InvalidOperationException($"{name} is not set: run agent/test.sh integration (agent/tests/integration/server.sh start)");

    private AgentRuntime NewRuntime(FakePlatform platform, string dataDir)
    {
        var options = new AgentOptions
        {
            EnrollEndpoint = _enrollEndpoint, RootCaPem = _rootCa, DataDirectory = dataDir, Locale = "ru", AgentVersion = "2.0.0",
            HealthPollInterval = TimeSpan.FromSeconds(1),
        };
        var store = new FileCredentialStore(Path.Combine(dataDir, "creds"), () => platform.GetDeviceInfo("2.0.0", false).HardwareUuid);
        return new AgentRuntime(options, store, platform, TrustAnchors.FromPem(_rootCa), new UpdateVerifier(new Dictionary<string, byte[]>()),
            installer: null, new XunitLoggerFactory(_out));
    }

    private async Task<string> OwnerToken()
    {
        using var resp = await _http.PostAsJsonAsync("/api/auth/login", new { phone = Env("ZAFIRO_IT_OWNER_PHONE"), password = Env("ZAFIRO_IT_PASSWORD") });
        resp.EnsureSuccessStatusCode();
        var json = await resp.Content.ReadFromJsonAsync<JsonObject>();
        return json!["access_token"]!.GetValue<string>();
    }

    private async Task<JsonObject> Panel(HttpMethod method, string path, string token, object? body = null)
    {
        using var req = new HttpRequestMessage(method, path);
        req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        if (body is not null) req.Content = JsonContent.Create(body);
        using var resp = await _http.SendAsync(req);
        var text = await resp.Content.ReadAsStringAsync();
        Assert.True(resp.IsSuccessStatusCode, $"{method} {path}: {(int)resp.StatusCode} {text}");
        return JsonNode.Parse(text) as JsonObject ?? new JsonObject();
    }

    private static async Task WaitUntil(Func<bool> condition, string what, int seconds = 20)
    {
        var deadline = DateTime.UtcNow.AddSeconds(seconds);
        while (!condition())
        {
            if (DateTime.UtcNow > deadline) Assert.Fail("timeout: " + what);
            await Task.Delay(100);
        }
    }

    /// <summary>
    /// Здоровье ПК (Status.health, §14.10): первый снимок уходит в Status сразу после подключения, отключение гарнитуры —
    /// Status с health сразу (не через status_interval 60 с); сервер принимает его, стрим не рвётся (команда из панели
    /// доходит после этого), панель показывает измерения (если её API уже есть на стенде).
    /// </summary>
    [Fact]
    public async Task Здоровье_ПК_доходит_до_сервера()
    {
        using var cts = new CancellationTokenSource(TimeSpan.FromMinutes(2));
        var owner = await OwnerToken();
        var zone = long.Parse(Env("ZAFIRO_IT_ZONE_ID"), System.Globalization.CultureInfo.InvariantCulture);
        var code = (await Panel(HttpMethod.Post, "/api/agent-enrollment/codes", owner, new { zone_id = zone }))["code"]!.GetValue<string>();

        var platform = new FakePlatform();
        await using var runtime = NewRuntime(platform, Path.Combine(_dir.Path, "health"));
        var sent = new List<(DateTime At, Status Status)>();
        runtime.StatusSent += s =>
        {
            if (s.Health is not null) lock (sent) sent.Add((DateTime.UtcNow, s));
        };
        var shown = new List<string>();
        runtime.UiEvent += e =>
        {
            if (e.KindCase == Event.KindOneofCase.ShowMessage) lock (shown) shown.Add(e.ShowMessage.Text);
        };
        await runtime.EnrollWithCodeAsync(code, cts.Token);
        var computerId = runtime.Credentials!.Stored.ComputerId;
        var run = runtime.RunAsync(cts.Token);
        await WaitUntil(() => runtime.State.Connection == Connection.Online, "Welcome");
        await WaitUntil(() => { lock (sent) return sent.Count >= 1; }, "first Status with health");
        Status first;
        lock (sent) first = sent[0].Status;
        Assert.Equal(650, first.Health.CpuTempDecicelsius);
        Assert.Equal(4, first.Health.Peripherals.Count);

        // Отключение гарнитуры: Status с health — через опрос (1 с) + не чаще 1 Status в 2 с, а не через 60 с.
        int before;
        lock (sent) before = sent.Count;
        var unplugged = DateTime.UtcNow;
        platform.FakeHealth.Unplug(PeripheralKind.Headset);
        await WaitUntil(() => { lock (sent) return sent.Count > before; }, "Status with health after the headset was unplugged", 15);
        (DateTime At, Status Status) urgent;
        lock (sent) urgent = sent[before];
        Assert.True(urgent.At - unplugged < TimeSpan.FromSeconds(10), $"health status came after {urgent.At - unplugged}");
        Assert.False(urgent.Status.Health.Peripherals.Single(p => p.Kind == PeripheralKind.Headset).Connected);

        // Сервер принял: стрим жив, команда из панели доходит.
        await Task.Delay(1500);
        Assert.Equal(Connection.Online, runtime.State.Connection);
        await Panel(HttpMethod.Post, "/api/club-computers/notify", owner, new { computer_ids = new[] { computerId }, message = "Проверка здоровья" });
        await WaitUntil(() => { lock (shown) return shown.Contains("Проверка здоровья"); }, "ShowMessage after health");

        // Панель (Этап 7, пишется параллельно): если API уже есть — те же измерения.
        using (var req = new HttpRequestMessage(HttpMethod.Get, $"/api/club-computers/{computerId}/health"))
        {
            req.Headers.Authorization = new AuthenticationHeaderValue("Bearer", owner);
            using var resp = await _http.SendAsync(req);
            var text = await resp.Content.ReadAsStringAsync();
            _out.WriteLine($"GET health: {(int)resp.StatusCode} {text}");
            if (resp.IsSuccessStatusCode && JsonNode.Parse(text)?["health"] is JsonObject health)
            {
                Assert.Equal(650, health["cpu_temp_deci"]!.GetValue<long>());
            }
        }

        await cts.CancelAsync();
        await run;
    }

    [Fact]
    public async Task Полный_сценарий_агента()
    {
        using var cts = new CancellationTokenSource(TimeSpan.FromMinutes(3));
        var owner = await OwnerToken();
        var zone = long.Parse(Env("ZAFIRO_IT_ZONE_ID"), System.Globalization.CultureInfo.InvariantCulture);

        // 1. Код регистрации — как сотрудник в панели «Подключить ПК».
        var issued = await Panel(HttpMethod.Post, "/api/agent-enrollment/codes", owner, new { zone_id = zone });
        var code = issued["code"]!.GetValue<string>();
        Assert.Matches("^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$", code);

        var platform = new FakePlatform();
        var dataDir = Path.Combine(_dir.Path, "agent");
        var runtime = NewRuntime(platform, dataDir);
        var handled = new List<(Command Cmd, CommandResult Result)>();
        runtime.CommandHandled += (c, r) =>
        {
            lock (handled) handled.Add((c, r));
        };
        var shown = new List<string>();
        runtime.UiEvent += e =>
        {
            if (e.KindCase == Event.KindOneofCase.ShowMessage) lock (shown) shown.Add(e.ShowMessage.Text);
        };

        // 2. Enroll по коду: сертификат до встроенного корня, CN=computer:<id>.
        var snap = await runtime.EnrollWithCodeAsync(code, cts.Token);
        var creds = runtime.Credentials!;
        var computerId = creds.Stored.ComputerId;
        Assert.True(computerId > 0);
        Assert.Equal(1, creds.Stored.ClubId);
        Assert.Contains($"CN=computer:{computerId}", creds.Certificate.Subject, StringComparison.Ordinal);
        Assert.Equal(Connection.Connecting, snap.Connection);

        // Тот же код второй раз — ENROLLMENT_CODE_EXPIRED с текстом для экрана.
        var reuse = await Assert.ThrowsAnyAsync<AgentRpcException>(() => NewRuntime(new FakePlatform(), Path.Combine(_dir.Path, "other")).EnrollWithCodeAsync(code, cts.Token));
        Assert.Equal(ErrorCode.EnrollmentCodeExpired, reuse.ErrorCode);
        Assert.False(string.IsNullOrEmpty(reuse.LocalizedMessage));

        // 3. Connect → Welcome → Status.
        var run = runtime.RunAsync(cts.Token);
        await WaitUntil(() => runtime.State.Connection == Connection.Online, "Welcome");
        var online = runtime.State.BuildSnapshot();
        Assert.Equal(computerId, online.Computer.ComputerId);
        Assert.Equal("Zafiro Arena", online.ClubName);
        Assert.Equal(ComputerState.Idle, online.State);
        Assert.Equal(Screen.Lock, online.Screen);

        // 4. Команда с сервера (уведомление на ПК из панели): ShowMessage доходит, итог подтверждён, seq сохранён.
        var notify = await Panel(HttpMethod.Post, "/api/club-computers/notify", owner, new { computer_ids = new[] { computerId }, message = "Проверка связи агента" });
        Assert.Equal(1, notify["sent"]!.GetValue<long>());
        await WaitUntil(() => { lock (shown) return shown.Contains("Проверка связи агента"); }, "ShowMessage");
        (Command Cmd, CommandResult Result) msg;
        lock (handled) msg = handled.Single(h => h.Cmd.KindCase == Command.KindOneofCase.ShowMessage);
        Assert.Equal(CommandStatus.Succeeded, msg.Result.Status);
        Assert.True(msg.Cmd.Seq > 0);
        Assert.Equal(msg.Cmd.Seq, runtime.Commands.LastCommandSeq);

        // 5. Переподключение (перезапуск службы): Hello с last_command_seq — команда не приходит второй раз.
        await cts.CancelAsync();
        await run;
        await runtime.DisposeAsync();
        using var cts2 = new CancellationTokenSource(TimeSpan.FromMinutes(3));
        runtime = NewRuntime(platform, dataDir);
        handled.Clear();
        runtime.CommandHandled += (c, r) =>
        {
            lock (handled) handled.Add((c, r));
        };
        Assert.Equal(msg.Cmd.Seq, runtime.Commands.LastCommandSeq);
        run = runtime.RunAsync(cts2.Token);
        await WaitUntil(() => runtime.State.Connection == Connection.Online, "Welcome after restart");
        await Task.Delay(1500);
        lock (handled) Assert.DoesNotContain(handled, h => h.Cmd.KindCase == Command.KindOneofCase.ShowMessage);

        // 5a. Продление сертификата (RenewCertificate новым ключом по mTLS) → переподключение с новым сертификатом,
        // прежний ключ удаляется после Welcome (§12.6 п. 6).
        var oldSerial = runtime.Credentials!.Certificate.SerialNumber;
        runtime.RenewSoon();
        await WaitUntil(() => runtime.Credentials is { } c && c.Certificate.SerialNumber != oldSerial, "RenewCertificate", 30);
        await WaitUntil(() => runtime.State.Connection == Connection.Online && runtime.Credentials!.Stored.PreviousKeyName.Length == 0, "reconnect with the renewed certificate", 30);
        var products = await runtime.Guest.ListProductsAsync(new ListProductsRequest(), cts2.Token);
        Assert.NotEmpty(products.Products);

        // 6. Вход гостя: токен остаётся в службе, ПК — LOCKED (гость без сессии).
        var login = await runtime.Guest.LoginAsync(new LoginRequest { Phone = Env("ZAFIRO_IT_GUEST_PHONE"), Password = Env("ZAFIRO_IT_PASSWORD") }, cts2.Token);
        var userId = login.SignIn.User.UserId;
        Assert.True(userId > 0);
        Assert.False(string.IsNullOrEmpty(runtime.State.GuestToken));
        Assert.Equal(ComputerState.Locked, runtime.State.ComputerState);
        var startMain = login.SignIn.Balance.MainTiyn;

        // Неверный пароль — INVALID_CREDENTIALS с текстом.
        var bad = await Assert.ThrowsAnyAsync<AgentRpcException>(() =>
            runtime.Guest.LoginAsync(new LoginRequest { Phone = Env("ZAFIRO_IT_GUEST_PHONE"), Password = "wrong-password" }, cts2.Token));
        Assert.Equal(ErrorCode.InvalidCredentials, bad.ErrorCode);
        Assert.False(string.IsNullOrEmpty(bad.LocalizedMessage));

        // 7. Тарифы и покупка (Status с signed_in_user_id уже на сервере — BalanceChanged придёт на этот ПК).
        await Task.Delay(2500);
        var tariffs = await runtime.Guest.ListTariffsAsync(new ListTariffsRequest(), cts2.Token);
        var tariffId = long.Parse(Env("ZAFIRO_IT_TARIFF_ID"), System.Globalization.CultureInfo.InvariantCulture);
        var tariff = tariffs.Tariffs.Single(t => t.TariffId == tariffId);
        var key = Guid.CreateVersion7().ToString();
        var buy = await runtime.Guest.PurchaseTariffAsync(new PurchaseTariffRequest { IdempotencyKey = key, TariffId = tariffId, BalanceSource = BalanceSource.Main }, cts2.Token);
        Assert.NotNull(buy.Purchase.Session);
        Assert.Equal(tariff.PriceTiyn, buy.Purchase.ChargedMainTiyn + buy.Purchase.ChargedBonusTiyn);
        Assert.Equal(startMain - buy.Purchase.ChargedMainTiyn, buy.Purchase.Balance.MainTiyn);
        Assert.Equal(ComputerState.InSession, runtime.State.ComputerState);

        // Повтор с тем же ключом (сетевой повтор) — тот же ответ, второго списания нет.
        var again = await runtime.Guest.PurchaseTariffAsync(new PurchaseTariffRequest { IdempotencyKey = key, TariffId = tariffId, BalanceSource = BalanceSource.Main }, cts2.Token);
        Assert.Equal(buy.Purchase.Session.SessionId, again.Purchase.Session.SessionId);
        Assert.Equal(buy.Purchase.Balance.MainTiyn, again.Purchase.Balance.MainTiyn);
        var profile = await runtime.Guest.GetProfileAsync(new GetProfileRequest(), cts2.Token);
        Assert.Equal(buy.Purchase.Balance.MainTiyn, profile.Balance.MainTiyn);

        // Тот же ключ с другим запросом — IDEMPOTENCY_KEY_REUSED.
        var reused = await Assert.ThrowsAnyAsync<AgentRpcException>(() =>
            runtime.Guest.PurchaseTariffAsync(new PurchaseTariffRequest { IdempotencyKey = key, TariffId = tariffId, BalanceSource = BalanceSource.Bonus }, cts2.Token));
        Assert.Equal(ErrorCode.IdempotencyKeyReused, reused.ErrorCode);

        // 8. BalanceChanged и StartSession от сервера после покупки.
        await WaitUntil(() =>
        {
            lock (handled) return handled.Any(h => h.Cmd.KindCase == Command.KindOneofCase.BalanceChanged && h.Cmd.BalanceChanged.UserId == userId);
        }, "BalanceChanged");
        lock (handled)
        {
            var bc = handled.Last(h => h.Cmd.KindCase == Command.KindOneofCase.BalanceChanged);
            Assert.Equal(buy.Purchase.Balance.MainTiyn, bc.Cmd.BalanceChanged.Balance.MainTiyn);
        }

        // 9. Гость завершает сессию — возврат неиспользованного времени, ПК снова LOCKED.
        var end = await runtime.Guest.EndSessionAsync(new EndSessionRequest { SessionId = buy.Purchase.Session.SessionId }, cts2.Token);
        Assert.True(end.RefundedMainTiyn + end.RefundedBonusTiyn >= 0);
        Assert.Equal(ComputerState.Locked, runtime.State.ComputerState);

        // 9a. Автобиллинг решает сервер (AG-12): состояние приходит по запросу/команде; гость сам завершил сессию — стоп.
        var ab = await runtime.Guest.GetAutoBillingAsync(new GetAutoBillingRequest(), cts2.Token);
        Assert.NotNull(ab.Status);
        Assert.False(ab.Status.Waiting && ab.Status.Stopped);

        // 9b. «На весь баланс» считает и покупает сервер (AG-12, Q-008): минуты — как в GetMinuteOffer.
        var offer = await runtime.Guest.GetMinuteOfferAsync(new GetMinuteOfferRequest(), cts2.Token);
        if (offer.WholeBalance && offer.WholeBalanceMinutes > 0)
        {
            var whole = await runtime.Guest.PlayWholeBalanceAsync(new PlayWholeBalanceRequest(), cts2.Token);
            Assert.Equal(offer.WholeBalanceMinutes, whole.Purchase.MinutesAdded);
            Assert.Equal(offer.WholeBalancePriceTiyn, whole.Purchase.ChargedMainTiyn);
            await runtime.Guest.EndSessionAsync(new EndSessionRequest { SessionId = whole.Purchase.Session.SessionId }, cts2.Token);
        }
        else
        {
            var refused = await Assert.ThrowsAnyAsync<AgentRpcException>(() => runtime.Guest.PlayWholeBalanceAsync(new PlayWholeBalanceRequest(), cts2.Token));
            Assert.Contains(refused.ErrorCode, new[] { ErrorCode.FeatureDisabled, ErrorCode.InsufficientFunds, ErrorCode.TariffUnavailable });
            Assert.False(string.IsNullOrEmpty(refused.LocalizedMessage));
        }

        // 9c. Картинка клуба по сертификату ПК (AG-13): есть — PNG/JPEG, нет — NOT_FOUND.
        try
        {
            var logo = await runtime.Guest.GetImageAsync(new GetImageRequest { Kind = ImageKind.ClubLogo }, cts2.Token);
            Assert.Contains(logo.ContentType, new[] { "image/png", "image/jpeg" });
        }
        catch (AgentRpcException e)
        {
            Assert.Equal(Grpc.Core.StatusCode.NotFound, e.StatusCode);
        }

        // 9d. Язык гостя уходит в zafiro-locale (AG-16): текст ошибки есть на любом языке.
        Assert.True(runtime.State.SetGuestLocale("en") || runtime.State.Config?.Languages.All(l => l.Code != "en") == true);
        var badEn = await Assert.ThrowsAnyAsync<AgentRpcException>(() =>
            runtime.Guest.LoginAsync(new LoginRequest { Phone = Env("ZAFIRO_IT_GUEST_PHONE"), Password = "wrong-password" }, cts2.Token));
        Assert.Equal(ErrorCode.InvalidCredentials, badEn.ErrorCode);
        Assert.False(string.IsNullOrEmpty(badEn.LocalizedMessage));
        runtime.State.SetGuestLocale("");

        // 10. Гость выходит: токен отозван на сервере, ПК свободен. (Без выхода токен этого ПК ещё 2 минуты считался бы
        // «вошёл на другом ПК» — AgentSignIns::elsewhere(), — даже после отзыва сертификата.)
        await runtime.Guest.LogoutAsync(new LogoutRequest(), cts2.Token);
        Assert.Null(runtime.State.GuestToken);
        Assert.Equal(ComputerState.Idle, runtime.State.ComputerState);
        var afterLogout = await Assert.ThrowsAnyAsync<AgentRpcException>(() => runtime.Guest.ListTariffsAsync(new ListTariffsRequest(), cts2.Token));
        Assert.Equal(ErrorCode.GuestTokenInvalid, afterLogout.ErrorCode);

        // 11. Отзыв сертификата («Отключить ПК»): стрим закрывается CERTIFICATE_REVOKED, ключ удалён.
        await Panel(HttpMethod.Post, $"/api/agent-enrollment/computers/{computerId}/revoke", owner);
        await WaitUntil(() => runtime.State.Connection == Connection.Revoked, "CERTIFICATE_REVOKED", 30);
        Assert.Null(runtime.Credentials);
        Assert.False(File.Exists(Path.Combine(dataDir, "creds", "credentials.pb")));

        await cts2.CancelAsync();
        await run;
        await runtime.DisposeAsync();
    }
}

/// <summary>Журнал службы в вывод теста.</summary>
internal sealed class XunitLoggerFactory(ITestOutputHelper output) : Microsoft.Extensions.Logging.ILoggerFactory
{
    public void AddProvider(Microsoft.Extensions.Logging.ILoggerProvider provider)
    {
    }

    public Microsoft.Extensions.Logging.ILogger CreateLogger(string categoryName) => new Logger(output, categoryName);

    public void Dispose()
    {
    }

    private sealed class Logger(ITestOutputHelper output, string category) : Microsoft.Extensions.Logging.ILogger
    {
        public IDisposable? BeginScope<TState>(TState state)
            where TState : notnull => null;

        public bool IsEnabled(Microsoft.Extensions.Logging.LogLevel logLevel) => logLevel >= Microsoft.Extensions.Logging.LogLevel.Information;

        public void Log<TState>(Microsoft.Extensions.Logging.LogLevel logLevel, Microsoft.Extensions.Logging.EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter)
        {
            try
            {
                output.WriteLine($"{DateTime.UtcNow:HH:mm:ss.fff} {logLevel} {category}: {formatter(state, exception)}");
            }
            catch (InvalidOperationException)
            {
                // тест уже завершён
            }
        }
    }
}
