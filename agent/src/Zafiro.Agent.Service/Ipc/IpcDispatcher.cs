using Google.Protobuf;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Guest;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Ipc;

/// <summary>
/// Методы IPC для UI (список — ipc.proto, Request): обёртки над <see cref="AgentRuntime"/> и <see cref="GuestApi"/>.
/// Таблица без рефлексии (Native AOT). Токен гостя в ответы не попадает (<see cref="GuestApi.Redacted(GuestSignIn)"/>).
/// </summary>
public sealed class IpcDispatcher
{
    private readonly Dictionary<string, Func<ByteString, CancellationToken, Task<IMessage>>> _methods = new(StringComparer.Ordinal);

    public IpcDispatcher(AgentRuntime runtime)
    {
        ArgumentNullException.ThrowIfNull(runtime);
        var g = runtime.Guest;

        Map("agent.GetSnapshot", GetSnapshotRequest.Parser, (_, _) => Task.FromResult(runtime.State.BuildSnapshot()));
        Map("agent.Enroll", EnrollWithCodeRequest.Parser, (r, ct) => runtime.EnrollWithCodeAsync(r.Code, ct));
        Map("agent.VerifyStaffPin", VerifyStaffPinRequest.Parser, async (r, ct) =>
        {
            var resp = await g.VerifyStaffPinAsync(r, ct).ConfigureAwait(false);
            var until = resp.Until?.ToDateTimeOffset() ?? DateTimeOffset.UtcNow.AddMinutes(resp.StaffMinutes > 0 ? resp.StaffMinutes : 15);
            runtime.EnterServiceMode(until);
            return resp;
        });
        Map("agent.GetTranslations", GetTranslationsRequest.Parser, (r, ct) => g.GetTranslationsAsync(r, ct));
        Map("agent.ReportActivity", ReportActivityRequest.Parser, (r, _) =>
        {
            runtime.ReportActivity(r.IdleSeconds);
            return Task.FromResult(new GetSnapshotRequest());
        });
        Map("agent.SignOut", SignOutRequest.Parser, async (_, ct) =>
        {
            await g.LogoutAsync(new LogoutRequest(), ct).ConfigureAwait(false);
            return runtime.State.BuildSnapshot();
        });

        Map("GuestAuthService/Login", LoginRequest.Parser, async (r, ct) =>
        {
            var resp = await g.LoginAsync(r, ct).ConfigureAwait(false);
            return new LoginResponse { SignIn = GuestApi.Redacted(resp.SignIn) };
        });
        Map("GuestAuthService/Register", RegisterRequest.Parser, async (r, ct) =>
        {
            var resp = await g.RegisterAsync(r, ct).ConfigureAwait(false);
            return new RegisterResponse { SignIn = GuestApi.Redacted(resp.SignIn), AccountCreated = resp.AccountCreated };
        });
        Map("GuestAuthService/CreateQrLogin", CreateQrLoginRequest.Parser, (r, ct) => g.CreateQrLoginAsync(r, ct));
        Map("GuestAuthService/WatchQrLogin", WatchQrLoginRequest.Parser, async (r, ct) => GuestApi.Redacted(await g.WatchQrLoginAsync(r, ct).ConfigureAwait(false)));
        Map("GuestAuthService/RequestPasswordResetCode", RequestPasswordResetCodeRequest.Parser, (r, ct) => g.RequestPasswordResetCodeAsync(r, ct));
        Map("GuestAuthService/ResetPassword", ResetPasswordRequest.Parser, (r, ct) => g.ResetPasswordAsync(r, ct));
        Map("GuestAuthService/Logout", LogoutRequest.Parser, (r, ct) => g.LogoutAsync(r, ct));

        Map("GuestService/GetProfile", GetProfileRequest.Parser, (r, ct) => g.GetProfileAsync(r, ct));
        Map("GuestService/UpdateName", UpdateNameRequest.Parser, (r, ct) => g.UpdateNameAsync(r, ct));
        Map("GuestService/ChangePassword", ChangePasswordRequest.Parser, (r, ct) => g.ChangePasswordAsync(r, ct));
        Map("GuestService/ListTariffs", ListTariffsRequest.Parser, (r, ct) => g.ListTariffsAsync(r, ct));
        Map("GuestService/PurchaseTariff", PurchaseTariffRequest.Parser, (r, ct) => g.PurchaseTariffAsync(r, ct));
        Map("GuestService/GetMinuteOffer", GetMinuteOfferRequest.Parser, (r, ct) => g.GetMinuteOfferAsync(r, ct));
        Map("GuestService/PurchaseMinutes", PurchaseMinutesRequest.Parser, (r, ct) => g.PurchaseMinutesAsync(r, ct));
        Map("GuestService/ListPackages", ListPackagesRequest.Parser, (r, ct) => g.ListPackagesAsync(r, ct));
        Map("GuestService/StartPackage", StartPackageRequest.Parser, (r, ct) => g.StartPackageAsync(r, ct));
        Map("GuestService/EndSession", EndSessionRequest.Parser, (r, ct) => g.EndSessionAsync(r, ct));
        Map("GuestService/GetSeatMap", GetSeatMapRequest.Parser, (r, ct) => g.GetSeatMapAsync(r, ct));
        Map("GuestService/TransferSeat", TransferSeatRequest.Parser, (r, ct) => g.TransferSeatAsync(r, ct));
        Map("GuestService/ListHistory", ListHistoryRequest.Parser, (r, ct) => g.ListHistoryAsync(r, ct));
        Map("GuestService/ListNews", ListNewsRequest.Parser, (r, ct) => g.ListNewsAsync(r, ct));

        Map("ShopService/ListProducts", ListProductsRequest.Parser, (r, ct) => g.ListProductsAsync(r, ct));
        Map("ShopService/PlaceOrder", PlaceOrderRequest.Parser, (r, ct) => g.PlaceOrderAsync(r, ct));

        Map("SocialService/ListFriends", ListFriendsRequest.Parser, (r, ct) => g.ListFriendsAsync(r, ct));
        Map("SocialService/AddFriend", AddFriendRequest.Parser, (r, ct) => g.AddFriendAsync(r, ct));
        Map("SocialService/AcceptFriend", AcceptFriendRequest.Parser, (r, ct) => g.AcceptFriendAsync(r, ct));
        Map("SocialService/RemoveFriend", RemoveFriendRequest.Parser, (r, ct) => g.RemoveFriendAsync(r, ct));
        Map("SocialService/RequestTransferCode", RequestTransferCodeRequest.Parser, (r, ct) => g.RequestTransferCodeAsync(r, ct));
        Map("SocialService/TransferBalance", TransferBalanceRequest.Parser, (r, ct) => g.TransferBalanceAsync(r, ct));
        Map("SocialService/ListChatMessages", ListChatMessagesRequest.Parser, (r, ct) => g.ListChatMessagesAsync(r, ct));
        Map("SocialService/SendChatMessage", SendChatMessageRequest.Parser, (r, ct) => g.SendChatMessageAsync(r, ct));
    }

    /// <summary>Имена методов (для тестов и документации).</summary>
    public IReadOnlyCollection<string> Methods => _methods.Keys;

    private void Map<TReq, TResp>(string name, MessageParser<TReq> parser, Func<TReq, CancellationToken, Task<TResp>> handler)
        where TReq : IMessage<TReq>
        where TResp : IMessage
    {
        _methods[name] = async (payload, ct) => await handler(parser.ParseFrom(payload), ct).ConfigureAwait(false);
    }

    /// <summary>Выполняет запрос UI; любая ошибка — Response.error (текст для гостя — LocalizedMessage сервера).</summary>
    public async Task<Response> DispatchAsync(Request request, CancellationToken ct)
    {
        ArgumentNullException.ThrowIfNull(request);
        var response = new Response { Id = request.Id };
        if (!_methods.TryGetValue(request.Method, out var handler))
        {
            response.Error = new Error { ErrorCode = ErrorCode.Unspecified, Message = "", GrpcStatus = (int)Grpc.Core.StatusCode.Unimplemented };
            return response;
        }

        try
        {
            var result = await handler(request.Payload, ct).ConfigureAwait(false);
            response.Payload = result.ToByteString();
        }
        catch (AgentRpcException e)
        {
            response.Error = ToError(e);
        }
        catch (InvalidProtocolBufferException)
        {
            response.Error = new Error { ErrorCode = ErrorCode.ValidationFailed, GrpcStatus = (int)Grpc.Core.StatusCode.InvalidArgument };
        }
        catch (ArgumentException)
        {
            response.Error = new Error { ErrorCode = ErrorCode.ValidationFailed, GrpcStatus = (int)Grpc.Core.StatusCode.InvalidArgument };
        }

        return response;
    }

    public static Error ToError(AgentRpcException e)
    {
        ArgumentNullException.ThrowIfNull(e);
        var err = new Error
        {
            ErrorCode = e.ErrorCode,
            GrpcStatus = (int)e.StatusCode,
            Message = e.LocalizedMessage ?? "",
            RetryAfterMs = e.RetryAfter is { } ra ? (long)ra.TotalMilliseconds : 0,
        };
        err.Violations.AddRange(e.FieldViolations.Select(v => new FieldViolation { Field = v.Field, Description = v.Description }));
        return err;
    }
}
