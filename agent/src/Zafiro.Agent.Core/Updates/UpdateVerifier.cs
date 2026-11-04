using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Updates;

/// <summary>Почему обновление отклонено.</summary>
public enum UpdateRejection
{
    None = 0,
    /// <summary>Поля UpdateAvailable не прошли проверку (версия, канал, sha256, размер, адрес).</summary>
    InvalidFields,
    /// <summary>key_id нет среди встроенных ключей.</summary>
    UnknownKey,
    /// <summary>Подпись Ed25519 не сходится.</summary>
    BadSignature,
    /// <summary>Хост установщика не из списка разрешённых.</summary>
    HostNotAllowed,
}

/// <summary>
/// Проверка подписи обновления агента (решение «Обновления агента», docs/DECISIONS.md; docs/agent-protocol.md §6).
/// Ed25519 (RFC 8032, «чистый», без предхеширования) по UTF-8 строке
/// <c>"zafiro-agent-update/v1\n" + version + "\n" + sha256 + "\n" + size_bytes + "\n" + channel</c> —
/// побайтно как <c>commands.UpdateSigningMessage</c> сервера (server/internal/agent/commands/update.go) и подпись
/// утилиты server/cmd/sign-update. Открытые ключи — встроенные в агент (key_id → 32 байта), не с сервера.
/// </summary>
public sealed class UpdateVerifier
{
    private readonly IReadOnlyDictionary<string, byte[]> _keys;
    private readonly IReadOnlyCollection<string> _allowedHosts;

    /// <param name="publicKeys">key_id → открытый ключ Ed25519 (32 байта).</param>
    /// <param name="allowedHosts">Хосты, с которых можно скачивать установщик (пусто — любой https-хост).</param>
    public UpdateVerifier(IReadOnlyDictionary<string, byte[]> publicKeys, IReadOnlyCollection<string>? allowedHosts = null)
    {
        ArgumentNullException.ThrowIfNull(publicKeys);
        foreach (var (id, key) in publicKeys)
        {
            if (key is null || key.Length != 32) throw new ArgumentException($"Ed25519 public key {id} must be 32 bytes.", nameof(publicKeys));
        }

        _keys = publicKeys;
        _allowedHosts = allowedHosts ?? [];
    }

    /// <summary>Ключи из строки конфигурации "key_id:base64[,key_id2:base64]" (как agent.update.public_keys сервера).</summary>
    public static IReadOnlyDictionary<string, byte[]> ParseKeys(string? spec)
    {
        var result = new Dictionary<string, byte[]>(StringComparer.Ordinal);
        if (string.IsNullOrWhiteSpace(spec)) return result;
        foreach (var part in spec.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries))
        {
            var i = part.IndexOf(':', StringComparison.Ordinal);
            if (i <= 0) throw new FormatException("Update key must be key_id:base64.");
            var key = Convert.FromBase64String(part[(i + 1)..]);
            if (key.Length != 32) throw new FormatException($"Update key {part[..i]} must be 32 bytes.");
            result[part[..i]] = key;
        }

        return result;
    }

    /// <summary>Подписываемая строка — байты UTF-8, LF, без завершающего перевода строки.</summary>
    public static byte[] SigningMessage(UpdateAvailable u)
    {
        ArgumentNullException.ThrowIfNull(u);
        return Encoding.UTF8.GetBytes("zafiro-agent-update/v1\n" + u.Version + "\n" + u.Sha256 + "\n" +
            u.SizeBytes.ToString(CultureInfo.InvariantCulture) + "\n" + u.Channel);
    }

    /// <summary>Проверка полей и подписи до скачивания. <see cref="UpdateRejection.None"/> — можно скачивать.</summary>
    public UpdateRejection Verify(UpdateAvailable u)
    {
        ArgumentNullException.ThrowIfNull(u);
        if (!ValidFields(u)) return UpdateRejection.InvalidFields;
        var url = new Uri(u.Url);
        if (_allowedHosts.Count > 0 && !_allowedHosts.Contains(url.Host, StringComparer.OrdinalIgnoreCase)) return UpdateRejection.HostNotAllowed;
        if (!_keys.TryGetValue(u.KeyId, out var pub)) return UpdateRejection.UnknownKey;
        var sig = u.SignatureEd25519.ToByteArray();
        var msg = SigningMessage(u);
        return Org.BouncyCastle.Math.EC.Rfc8032.Ed25519.Verify(sig, 0, pub, 0, msg, 0, msg.Length)
            ? UpdateRejection.None
            : UpdateRejection.BadSignature;
    }

    /// <summary>Те же правила, что validateUpdate сервера (update.go).</summary>
    public static bool ValidFields(UpdateAvailable u)
    {
        ArgumentNullException.ThrowIfNull(u);
        if (string.IsNullOrEmpty(u.Version) || u.Version.Length > 64 || u.Version.AsSpan().IndexOfAny('\n', '\r') >= 0) return false;
        if (string.IsNullOrEmpty(u.Channel) || u.Channel.Length > 32 || u.Channel.AsSpan().IndexOfAny('\n', '\r') >= 0) return false;
        if (!IsLowerHex(u.Sha256, 64)) return false;
        if (u.SizeBytes <= 0) return false;
        if (!Uri.TryCreate(u.Url, UriKind.Absolute, out var url) || url.Scheme != Uri.UriSchemeHttps || string.IsNullOrEmpty(url.Host)
            || !string.IsNullOrEmpty(url.UserInfo)) return false;
        if (u.SignatureEd25519.Length != 64) return false;
        return !string.IsNullOrEmpty(u.KeyId);
    }

    /// <summary>Проверка скачанного файла: размер и SHA-256 (hex, нижний регистр) — по открытому потоку того же файла, что будет запущен.</summary>
    public static async Task<bool> VerifyFileAsync(Stream file, UpdateAvailable u, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(file);
        ArgumentNullException.ThrowIfNull(u);
        if (file.Length != u.SizeBytes) return false;
        file.Position = 0;
        var hash = await SHA256.HashDataAsync(file, cancellationToken).ConfigureAwait(false);
        var hex = Convert.ToHexStringLower(hash);
        return CryptographicOperations.FixedTimeEquals(Encoding.ASCII.GetBytes(hex), Encoding.ASCII.GetBytes(u.Sha256));
    }

    private static bool IsLowerHex(string s, int n)
    {
        if (s is null || s.Length != n) return false;
        foreach (var c in s)
        {
            if (c is not ((>= '0' and <= '9') or (>= 'a' and <= 'f'))) return false;
        }

        return true;
    }
}
