using System.Globalization;
using Google.Protobuf;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.UI.Bridge;

/// <summary>
/// Картинки клуба для страницы (AG-13): <c>https://img.zafiro.invalid/logo</c>, <c>/signin</c>, <c>/news/&lt;id&gt;</c>.
/// Процесс UI отвечает на такие запросы WebView2 (WebResourceRequested) байтами из службы (IPC agent.GetImage → кэш
/// службы → AgentService.GetImage), сам в сеть не ходит. CSP страницы разрешает <c>img-src</c> только себя, data: и этот
/// хост. Метод agent.GetImage напрямую со страницы не вызывается (байты не идут через JSON-мост).
/// </summary>
public sealed partial class BridgeRouter
{
    /// <summary>Виртуальный хост картинок клуба (.invalid — не разрешается в сети).</summary>
    public const string ImageHost = "img.zafiro.invalid";

    /// <summary>Начало адреса картинок.</summary>
    public const string ImageOrigin = "https://" + ImageHost + "/";

    /// <summary>Адрес картинки → запрос службы; null — не адрес картинки клуба.</summary>
    public static GetImageRequest? ParseImageUri(string? uri)
    {
        if (uri is null || !uri.StartsWith(ImageOrigin, StringComparison.Ordinal)) return null;
        var path = uri[ImageOrigin.Length..];
        var q = path.IndexOfAny(['?', '#']);
        if (q >= 0) path = path[..q];
        if (path == "logo") return new GetImageRequest { Kind = ImageKind.ClubLogo };
        if (path == "signin") return new GetImageRequest { Kind = ImageKind.Signin };
        if (path.StartsWith("news/", StringComparison.Ordinal)
            && path.Length is > 5 and <= 24
            && path.AsSpan(5).IndexOfAnyExceptInRange('0', '9') < 0
            && long.TryParse(path.AsSpan(5), NumberStyles.None, CultureInfo.InvariantCulture, out var id) && id > 0)
            return new GetImageRequest { Kind = ImageKind.News, Id = id };
        return null;
    }

    /// <summary>Картинка для запроса WebView2; null — нет (страница покажет встроенную заглушку).</summary>
    public async Task<GetImageResponse?> GetImageAsync(string uri, CancellationToken cancellationToken)
    {
        var request = ParseImageUri(uri);
        if (request is null) return null;
        try
        {
            var response = await _ipc.CallAsync("agent.GetImage", request.ToByteString(), cancellationToken).ConfigureAwait(false);
            if (response.Error is not null) return null;
            var image = GetImageResponse.Parser.ParseFrom(response.Payload);
            return image.Data.Length > 0 && image.ContentType is "image/png" or "image/jpeg" ? image : null;
        }
        catch (Exception e) when (e is IOException or TimeoutException or InvalidProtocolBufferException)
        {
            return null;
        }
    }
}
