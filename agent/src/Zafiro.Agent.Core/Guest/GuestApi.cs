using Grpc.Core;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Guest;

/// <summary>Реакции службы на действия гостя (события стрима, состояние для UI).</summary>
public interface IGuestEvents
{
    /// <summary>Гость вошёл (Login / Register / QR).</summary>
    void SignedIn(GuestSignIn signIn, SignInMethod method);

    /// <summary>Гость вышел (сам или токен отклонён сервером).</summary>
    void SignedOut(long userId, SignOutReason reason);
}

/// <summary>
/// Unary-клиенты гостя (GuestAuthService, GuestService, ShopService, SocialService) поверх текущего mTLS-канала
/// службы. Токен гостя — только в памяти службы (<see cref="AgentState.GuestToken"/>) и уходит в метаданных
/// <c>authorization: Bearer …</c> (agent.proto, шапка п. 1). Денежные RPC несут idempotency_key: пустой ключ
/// заполняется новым UUID, и при сетевых повторах уходит тот же ключ (п. 5) — сервер не спишет дважды.
/// Ошибки — типизированные <see cref="AgentRpcException"/>; GUEST_TOKEN_INVALID выполняет выход гостя.
/// </summary>
public sealed class GuestApi
{
    /// <summary>Сколько раз денежный RPC повторяется при повторяемой ошибке (тем же ключом).</summary>
    public const int MoneyAttempts = 3;

    private readonly Func<CallInvoker?> _invoker;
    private readonly AgentState _state;
    private readonly AgentOptions _options;
    private readonly TimeProvider _time;
    private readonly IGuestEvents _events;

    public GuestApi(Func<CallInvoker?> invoker, AgentState state, AgentOptions options, TimeProvider time, IGuestEvents events)
    {
        _invoker = invoker;
        _state = state;
        _options = options;
        _time = time;
        _events = events;
    }

    // ------------------------------------------------------------------ GuestAuthService

    public async Task<LoginResponse> LoginAsync(LoginRequest request, CancellationToken ct = default)
    {
        var r = await Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).LoginAsync(request, o), write: true, token: false, ct).ConfigureAwait(false);
        OnSignIn(r.SignIn, SignInMethod.Password);
        return r;
    }

    public async Task<RegisterResponse> RegisterAsync(RegisterRequest request, CancellationToken ct = default)
    {
        var r = await Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).RegisterAsync(request, o), write: true, token: false, ct).ConfigureAwait(false);
        OnSignIn(r.SignIn, SignInMethod.Registration);
        return r;
    }

    public Task<CreateQrLoginResponse> CreateQrLoginAsync(CreateQrLoginRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).CreateQrLoginAsync(request, o), write: true, token: false, ct);

    /// <summary>Ждёт подтверждения QR-входа (серверный стрим). Возвращает последний ответ: APPROVED (вход выполнен) или EXPIRED.</summary>
    public async Task<WatchQrLoginResponse> WatchQrLoginAsync(WatchQrLoginRequest request, CancellationToken ct = default)
    {
        var invoker = Invoker();
        try
        {
            using var call = new GuestAuthService.GuestAuthServiceClient(invoker).WatchQrLogin(request, Options(write: false, token: false, ct, deadline: TimeSpan.FromMinutes(10)));
            WatchQrLoginResponse last = new() { Status = QrLoginStatus.Expired };
            while (await call.ResponseStream.MoveNext(ct).ConfigureAwait(false))
            {
                last = call.ResponseStream.Current;
                if (last.Status == QrLoginStatus.Approved)
                {
                    OnSignIn(last.SignIn, SignInMethod.Qr);
                    return Redacted(last);
                }

                if (last.Status == QrLoginStatus.Expired) return last;
            }

            return last;
        }
        catch (RpcException ex)
        {
            throw RpcErrors.Parse(ex);
        }
    }

    public Task<RequestPasswordResetCodeResponse> RequestPasswordResetCodeAsync(RequestPasswordResetCodeRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).RequestPasswordResetCodeAsync(request, o), write: true, token: false, ct);

    public Task<ResetPasswordResponse> ResetPasswordAsync(ResetPasswordRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).ResetPasswordAsync(request, o), write: true, token: false, ct);

    /// <summary>Выход гостя: токен отзывается на сервере; локально — в любом случае.</summary>
    public async Task<LogoutResponse> LogoutAsync(LogoutRequest request, CancellationToken ct = default)
    {
        try
        {
            return await Call((i, o) => new GuestAuthService.GuestAuthServiceClient(i).LogoutAsync(request, o), write: true, token: true, ct).ConfigureAwait(false);
        }
        finally
        {
            var user = _state.SignOut();
            if (user > 0) _events.SignedOut(user, SignOutReason.User);
        }
    }

    // ------------------------------------------------------------------ GuestService

    public async Task<GetProfileResponse> GetProfileAsync(GetProfileRequest request, CancellationToken ct = default)
    {
        var r = await Call((i, o) => new GuestService.GuestServiceClient(i).GetProfileAsync(request, o), write: false, token: true, ct).ConfigureAwait(false);
        if (r.Balance is not null) _state.SetBalance(r.User?.UserId ?? 0, r.Balance);
        return r;
    }

    public Task<UpdateNameResponse> UpdateNameAsync(UpdateNameRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).UpdateNameAsync(request, o), write: true, token: true, ct);

    public Task<ChangePasswordResponse> ChangePasswordAsync(ChangePasswordRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).ChangePasswordAsync(request, o), write: true, token: true, ct);

    public Task<ListTariffsResponse> ListTariffsAsync(ListTariffsRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).ListTariffsAsync(request, o), write: false, token: true, ct);

    public async Task<PurchaseTariffResponse> PurchaseTariffAsync(PurchaseTariffRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var r = await Money((i, o) => new GuestService.GuestServiceClient(i).PurchaseTariffAsync(request, o), ct).ConfigureAwait(false);
        OnPurchase(r.Purchase);
        return r;
    }

    public Task<GetMinuteOfferResponse> GetMinuteOfferAsync(GetMinuteOfferRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).GetMinuteOfferAsync(request, o), write: false, token: true, ct);

    public async Task<PurchaseMinutesResponse> PurchaseMinutesAsync(PurchaseMinutesRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var r = await Money((i, o) => new GuestService.GuestServiceClient(i).PurchaseMinutesAsync(request, o), ct).ConfigureAwait(false);
        OnPurchase(r.Purchase);
        return r;
    }

    public Task<ListPackagesResponse> ListPackagesAsync(ListPackagesRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).ListPackagesAsync(request, o), write: false, token: true, ct);

    public async Task<StartPackageResponse> StartPackageAsync(StartPackageRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var r = await Money((i, o) => new GuestService.GuestServiceClient(i).StartPackageAsync(request, o), ct).ConfigureAwait(false);
        OnPurchase(r.Purchase);
        return r;
    }

    /// <summary>Гость завершает сессию досрочно (возврат неиспользованного времени).</summary>
    public async Task<EndSessionResponse> EndSessionAsync(EndSessionRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        if (string.IsNullOrEmpty(request.SessionId)) request.SessionId = _state.Session?.SessionId ?? "";
        var r = await Money((i, o) => new GuestService.GuestServiceClient(i).EndSessionAsync(request, o), ct).ConfigureAwait(false);
        _state.EndSession(request.SessionId);
        if (r.Balance is not null) _state.SetBalance(_state.SignedInUserId, r.Balance);
        return r;
    }

    public Task<GetSeatMapResponse> GetSeatMapAsync(GetSeatMapRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).GetSeatMapAsync(request, o), write: false, token: true, ct);

    public async Task<TransferSeatResponse> TransferSeatAsync(TransferSeatRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var sessionBefore = _state.Session?.SessionId ?? "";
        var r = await Money((i, o) => new GuestService.GuestServiceClient(i).TransferSeatAsync(request, o), ct).ConfigureAwait(false);
        _state.EndSession(sessionBefore); // сессия ушла на другой ПК
        return r;
    }

    public Task<ListHistoryResponse> ListHistoryAsync(ListHistoryRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).ListHistoryAsync(request, o), write: false, token: true, ct);

    public Task<ListNewsResponse> ListNewsAsync(ListNewsRequest request, CancellationToken ct = default) =>
        Call((i, o) => new GuestService.GuestServiceClient(i).ListNewsAsync(request, o), write: false, token: true, ct);

    // ------------------------------------------------------------------ ShopService

    public Task<ListProductsResponse> ListProductsAsync(ListProductsRequest request, CancellationToken ct = default) =>
        Call((i, o) => new ShopService.ShopServiceClient(i).ListProductsAsync(request, o), write: false, token: false, ct);

    public async Task<PlaceOrderResponse> PlaceOrderAsync(PlaceOrderRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var r = await Money((i, o) => new ShopService.ShopServiceClient(i).PlaceOrderAsync(request, o), ct).ConfigureAwait(false);
        if (r.Balance is not null) _state.SetBalance(_state.SignedInUserId, r.Balance);
        return r;
    }

    // ------------------------------------------------------------------ SocialService

    public Task<ListFriendsResponse> ListFriendsAsync(ListFriendsRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).ListFriendsAsync(request, o), write: false, token: true, ct);

    public Task<AddFriendResponse> AddFriendAsync(AddFriendRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).AddFriendAsync(request, o), write: true, token: true, ct);

    public Task<AcceptFriendResponse> AcceptFriendAsync(AcceptFriendRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).AcceptFriendAsync(request, o), write: true, token: true, ct);

    public Task<RemoveFriendResponse> RemoveFriendAsync(RemoveFriendRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).RemoveFriendAsync(request, o), write: true, token: true, ct);

    public Task<RequestTransferCodeResponse> RequestTransferCodeAsync(RequestTransferCodeRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).RequestTransferCodeAsync(request, o), write: true, token: true, ct);

    public async Task<TransferBalanceResponse> TransferBalanceAsync(TransferBalanceRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        var r = await Money((i, o) => new SocialService.SocialServiceClient(i).TransferBalanceAsync(request, o), ct).ConfigureAwait(false);
        if (r.Balance is not null) _state.SetBalance(_state.SignedInUserId, r.Balance);
        return r;
    }

    public Task<ListChatMessagesResponse> ListChatMessagesAsync(ListChatMessagesRequest request, CancellationToken ct = default) =>
        Call((i, o) => new SocialService.SocialServiceClient(i).ListChatMessagesAsync(request, o), write: false, token: true, ct);

    public Task<SendChatMessageResponse> SendChatMessageAsync(SendChatMessageRequest request, CancellationToken ct = default)
    {
        ArgumentNullException.ThrowIfNull(request);
        request.IdempotencyKey = Key(request.IdempotencyKey);
        return Money((i, o) => new SocialService.SocialServiceClient(i).SendChatMessageAsync(request, o), ct);
    }

    // ------------------------------------------------------------------ AgentService (unary)

    public Task<GetTranslationsResponse> GetTranslationsAsync(GetTranslationsRequest request, CancellationToken ct = default) =>
        Call((i, o) => new AgentService.AgentServiceClient(i).GetTranslationsAsync(request, o), write: false, token: false, ct);

    /// <summary>PIN сотрудника; если гость вошёл — токен уходит тоже (сервер пишет в журнал, при ком закрыли агент).</summary>
    public Task<VerifyStaffPinResponse> VerifyStaffPinAsync(VerifyStaffPinRequest request, CancellationToken ct = default) =>
        Call((i, o) => new AgentService.AgentServiceClient(i).VerifyStaffPinAsync(request, o), write: true, token: _state.GuestToken is not null, ct);

    // ------------------------------------------------------------------ общее

    /// <summary>GuestSignIn без токена — для UI (токен остаётся в службе).</summary>
    public static GuestSignIn Redacted(GuestSignIn signIn)
    {
        ArgumentNullException.ThrowIfNull(signIn);
        var copy = signIn.Clone();
        copy.Token = null;
        return copy;
    }

    public static WatchQrLoginResponse Redacted(WatchQrLoginResponse r)
    {
        ArgumentNullException.ThrowIfNull(r);
        var copy = r.Clone();
        if (copy.SignIn is not null) copy.SignIn = Redacted(copy.SignIn);
        return copy;
    }

    /// <summary>UUID ключа идемпотентности: переданный (повтор действия) или новый.</summary>
    public static string Key(string? key) => string.IsNullOrEmpty(key) ? Guid.CreateVersion7().ToString() : key;

    private void OnSignIn(GuestSignIn? signIn, SignInMethod method)
    {
        if (signIn?.Token is null || string.IsNullOrEmpty(signIn.Token.AccessToken)) return;
        _state.SignIn(signIn);
        _events.SignedIn(signIn, method);
    }

    private void OnPurchase(TimePurchase? p)
    {
        if (p is null) return;
        if (p.Session is not null) _state.StartSession(p.Session);
        if (p.Balance is not null) _state.SetBalance(_state.SignedInUserId, p.Balance);
    }

    private CallInvoker Invoker() =>
        _invoker() ?? throw new AgentRpcException(StatusCode.Unavailable, ErrorCode.Unspecified, null, "agent is not connected",
            null, new Dictionary<string, string>(), [], null);

    private CallOptions Options(bool write, bool token, CancellationToken ct, TimeSpan? deadline = null)
    {
        var headers = new Metadata();
        if (token)
        {
            var t = _state.GuestToken ?? throw new GuestTokenInvalidException(new AgentRpcException(StatusCode.Unauthenticated,
                ErrorCode.GuestTokenInvalid, null, "guest is not signed in", null, new Dictionary<string, string>(), [], null));
            headers.Add("authorization", "Bearer " + t);
        }

        var d = deadline ?? (write ? _options.WriteDeadline : _options.ReadDeadline);
        return new CallOptions(headers: headers, deadline: _time.GetUtcNow().UtcDateTime + d, cancellationToken: ct);
    }

    private async Task<T> Call<T>(Func<CallInvoker, CallOptions, AsyncUnaryCall<T>> call, bool write, bool token, CancellationToken ct)
    {
        var options = Options(write, token, ct); // нет токена — ошибка сразу, без сети
        var invoker = Invoker();
        var userBefore = _state.SignedInUserId;
        try
        {
            using var c = call(invoker, options);
            return await c.ResponseAsync.ConfigureAwait(false);
        }
        catch (RpcException ex)
        {
            var parsed = RpcErrors.Parse(ex);
            if (token && parsed is GuestTokenInvalidException)
            {
                var user = _state.SignOut(userBefore);
                if (user > 0) _events.SignedOut(user, SignOutReason.TokenInvalid);
            }

            throw parsed;
        }
    }

    /// <summary>Денежный RPC: повтор тем же ключом при UNAVAILABLE / DEADLINE_EXCEEDED / IDEMPOTENCY_IN_PROGRESS.</summary>
    private async Task<T> Money<T>(Func<CallInvoker, CallOptions, AsyncUnaryCall<T>> call, CancellationToken ct)
    {
        for (var attempt = 1; ; attempt++)
        {
            try
            {
                return await Call(call, write: true, token: true, ct).ConfigureAwait(false);
            }
            catch (AgentRpcException e) when (attempt < MoneyAttempts && !ct.IsCancellationRequested && RetrySameKey(e))
            {
                var delay = e.RetryAfter ?? TimeSpan.FromMilliseconds(500 * attempt);
                await Task.Delay(delay, _time, ct).ConfigureAwait(false);
            }
        }
    }

    private static bool RetrySameKey(AgentRpcException e) =>
        e.ErrorCode == ErrorCode.IdempotencyInProgress
        || (e.ErrorCode is ErrorCode.Unspecified or ErrorCode.Internal && e.StatusCode is StatusCode.Unavailable or StatusCode.DeadlineExceeded);
}
