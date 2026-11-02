using Google.Protobuf.Reflection;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Ipc;

/// <summary>Метод IPC: имя (ipc.proto, Request.method) и типы запроса и ответа.</summary>
public sealed record IpcMethod(string Name, MessageDescriptor Request, MessageDescriptor Response)
{
    /// <summary>Денежный RPC: запрос содержит idempotency_key (служба заполняет пустой UUID v7).</summary>
    public bool HasIdempotencyKey => Request.FindFieldByName("idempotency_key") is not null;
}

/// <summary>
/// Таблица методов IPC служба ↔ UI (ipc.proto, Request): одна на службу (IpcDispatcher) и UI (мост WebView2 принимает
/// только эти методы и только с этими схемами). Без рефлексии — дескрипторы сгенерированных сообщений.
/// </summary>
public static class IpcMethods
{
    private static readonly IpcMethod[] List =
    [
        new("agent.GetSnapshot", GetSnapshotRequest.Descriptor, Snapshot.Descriptor),
        new("agent.Enroll", EnrollWithCodeRequest.Descriptor, Snapshot.Descriptor),
        new("agent.VerifyStaffPin", VerifyStaffPinRequest.Descriptor, VerifyStaffPinResponse.Descriptor),
        new("agent.GetTranslations", GetTranslationsRequest.Descriptor, GetTranslationsResponse.Descriptor),
        new("agent.ReportActivity", ReportActivityRequest.Descriptor, GetSnapshotRequest.Descriptor),
        new("agent.SignOut", SignOutRequest.Descriptor, Snapshot.Descriptor),

        new("GuestAuthService/Login", LoginRequest.Descriptor, LoginResponse.Descriptor),
        new("GuestAuthService/Register", RegisterRequest.Descriptor, RegisterResponse.Descriptor),
        new("GuestAuthService/CreateQrLogin", CreateQrLoginRequest.Descriptor, CreateQrLoginResponse.Descriptor),
        new("GuestAuthService/WatchQrLogin", WatchQrLoginRequest.Descriptor, WatchQrLoginResponse.Descriptor),
        new("GuestAuthService/RequestPasswordResetCode", RequestPasswordResetCodeRequest.Descriptor, RequestPasswordResetCodeResponse.Descriptor),
        new("GuestAuthService/ResetPassword", ResetPasswordRequest.Descriptor, ResetPasswordResponse.Descriptor),
        new("GuestAuthService/Logout", LogoutRequest.Descriptor, LogoutResponse.Descriptor),

        new("GuestService/GetProfile", GetProfileRequest.Descriptor, GetProfileResponse.Descriptor),
        new("GuestService/UpdateName", UpdateNameRequest.Descriptor, UpdateNameResponse.Descriptor),
        new("GuestService/ChangePassword", ChangePasswordRequest.Descriptor, ChangePasswordResponse.Descriptor),
        new("GuestService/ListTariffs", ListTariffsRequest.Descriptor, ListTariffsResponse.Descriptor),
        new("GuestService/PurchaseTariff", PurchaseTariffRequest.Descriptor, PurchaseTariffResponse.Descriptor),
        new("GuestService/GetMinuteOffer", GetMinuteOfferRequest.Descriptor, GetMinuteOfferResponse.Descriptor),
        new("GuestService/PurchaseMinutes", PurchaseMinutesRequest.Descriptor, PurchaseMinutesResponse.Descriptor),
        new("GuestService/ListPackages", ListPackagesRequest.Descriptor, ListPackagesResponse.Descriptor),
        new("GuestService/StartPackage", StartPackageRequest.Descriptor, StartPackageResponse.Descriptor),
        new("GuestService/EndSession", EndSessionRequest.Descriptor, EndSessionResponse.Descriptor),
        new("GuestService/GetSeatMap", GetSeatMapRequest.Descriptor, GetSeatMapResponse.Descriptor),
        new("GuestService/TransferSeat", TransferSeatRequest.Descriptor, TransferSeatResponse.Descriptor),
        new("GuestService/ListHistory", ListHistoryRequest.Descriptor, ListHistoryResponse.Descriptor),
        new("GuestService/ListNews", ListNewsRequest.Descriptor, ListNewsResponse.Descriptor),

        new("ShopService/ListProducts", ListProductsRequest.Descriptor, ListProductsResponse.Descriptor),
        new("ShopService/PlaceOrder", PlaceOrderRequest.Descriptor, PlaceOrderResponse.Descriptor),

        new("SocialService/ListFriends", ListFriendsRequest.Descriptor, ListFriendsResponse.Descriptor),
        new("SocialService/AddFriend", AddFriendRequest.Descriptor, AddFriendResponse.Descriptor),
        new("SocialService/AcceptFriend", AcceptFriendRequest.Descriptor, AcceptFriendResponse.Descriptor),
        new("SocialService/RemoveFriend", RemoveFriendRequest.Descriptor, RemoveFriendResponse.Descriptor),
        new("SocialService/RequestTransferCode", RequestTransferCodeRequest.Descriptor, RequestTransferCodeResponse.Descriptor),
        new("SocialService/TransferBalance", TransferBalanceRequest.Descriptor, TransferBalanceResponse.Descriptor),
        new("SocialService/ListChatMessages", ListChatMessagesRequest.Descriptor, ListChatMessagesResponse.Descriptor),
        new("SocialService/SendChatMessage", SendChatMessageRequest.Descriptor, SendChatMessageResponse.Descriptor),
    ];

    /// <summary>Методы по имени.</summary>
    public static IReadOnlyDictionary<string, IpcMethod> All { get; } = List.ToDictionary(m => m.Name, StringComparer.Ordinal);
}
