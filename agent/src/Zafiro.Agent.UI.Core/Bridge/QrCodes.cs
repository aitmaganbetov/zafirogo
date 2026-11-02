using QRCoder;

namespace Zafiro.Agent.UI.Bridge;

/// <summary>
/// QR-код входа с телефона (ссылка из GuestAuthService/CreateQrLogin) — картинкой data: (страница не ходит в сеть).
/// Перенесено из старого агента: Views/ClubSetupWindow.xaml.cs QrBitmap() (QRCoder, уровень коррекции M).
/// </summary>
public static class QrCodes
{
    public const int MaxTextLength = 512;

    /// <summary>Только https-ссылки (сервер выдаёт https://…/pc-login/&lt;код&gt;).</summary>
    public static bool IsAllowed(string text) =>
        text.Length <= MaxTextLength && Uri.TryCreate(text, UriKind.Absolute, out var uri) && uri.Scheme == Uri.UriSchemeHttps;

    public static string ToPngDataUrl(string text)
    {
        using var generator = new QRCodeGenerator();
        using var data = generator.CreateQrCode(text, QRCodeGenerator.ECCLevel.M);
        var png = new PngByteQRCode(data).GetGraphic(8);
        return "data:image/png;base64," + Convert.ToBase64String(png);
    }
}
