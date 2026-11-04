using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;

namespace Zafiro.Agent.Core.Transport;

/// <summary>
/// Корни CA Zafiro, встроенные в агент (pinning, docs/agent-protocol.md §3): по ним проверяются сервер и
/// выданный при регистрации сертификат ПК. Системное хранилище Windows не используется.
/// </summary>
public sealed class TrustAnchors : IDisposable
{
    private static readonly Oid ClientAuth = new("1.3.6.1.5.5.7.3.2");

    public TrustAnchors(X509Certificate2Collection roots)
    {
        ArgumentNullException.ThrowIfNull(roots);
        if (roots.Count == 0) throw new ArgumentException("At least one pinned root CA is required.", nameof(roots));
        Roots = roots;
    }

    /// <summary>Доверенные корни.</summary>
    public X509Certificate2Collection Roots { get; }

    /// <summary>Корни из настроек: <see cref="AgentOptions.RootCaPem"/> или файл <see cref="AgentOptions.RootCaFile"/>.</summary>
    public static TrustAnchors FromOptions(AgentOptions options)
    {
        ArgumentNullException.ThrowIfNull(options);
        var pem = options.RootCaPem;
        if (string.IsNullOrWhiteSpace(pem) && !string.IsNullOrWhiteSpace(options.RootCaFile)) pem = File.ReadAllText(options.RootCaFile);
        if (string.IsNullOrWhiteSpace(pem)) throw new InvalidOperationException("Pinned root CA is not configured (RootCaPem / RootCaFile).");
        return FromPem(pem);
    }

    /// <summary>
    /// Корни из PEM (один или несколько — на время ротации корня). Склейка файлов без перевода строки
    /// (<c>cat old.pem new.pem</c>, когда в конце первого нет \n) тоже читается: иначе ImportFromPem не видит ни одного
    /// сертификата.
    /// </summary>
    public static TrustAnchors FromPem(string pem)
    {
        ArgumentNullException.ThrowIfNull(pem);
        var roots = new X509Certificate2Collection();
        roots.ImportFromPem(pem.Replace("-----END CERTIFICATE----------BEGIN", "-----END CERTIFICATE-----\n-----BEGIN", StringComparison.Ordinal));
        foreach (var r in roots)
        {
            if (!string.Equals(r.Subject, r.Issuer, StringComparison.Ordinal))
                throw new InvalidOperationException("Pinned root CA must be self-signed: " + r.Subject);
        }

        return new TrustAnchors(roots);
    }

    /// <summary>Политика проверки сервера для SslStream: только встроенные корни, без проверки отзыва (CRL/OCSP не нужны, §3).</summary>
    public X509ChainPolicy ServerPolicy()
    {
        var policy = new X509ChainPolicy
        {
            TrustMode = X509ChainTrustMode.CustomRootTrust,
            RevocationMode = X509RevocationMode.NoCheck,
        };
        policy.CustomTrustStore.AddRange(Roots);
        return policy;
    }

    /// <summary>
    /// Проверяет сертификат ПК из ответа Enroll/RenewCertificate: цепочка до встроенного корня (чужая цепочка не
    /// принимается), срок, EKU clientAuth, открытый ключ — тот, что агент отправил в CSR. Возвращает промежуточные CA
    /// (для отправки вместе с сертификатом в TLS).
    /// </summary>
    public X509Certificate2Collection ValidateIssued(X509Certificate2 leaf, IEnumerable<string> chainPem, ECDsa expectedKey, DateTimeOffset now)
    {
        ArgumentNullException.ThrowIfNull(leaf);
        ArgumentNullException.ThrowIfNull(chainPem);
        ArgumentNullException.ThrowIfNull(expectedKey);

        using (var leafKey = leaf.GetECDsaPublicKey())
        {
            if (leafKey is null || !leafKey.ExportSubjectPublicKeyInfo().AsSpan().SequenceEqual(expectedKey.ExportSubjectPublicKeyInfo()))
                throw new CryptographicException("Issued certificate does not match the agent key.");
        }

        var intermediates = new X509Certificate2Collection();
        foreach (var pem in chainPem)
        {
            var c = new X509Certificate2Collection();
            c.ImportFromPem(pem);
            foreach (var cert in c)
            {
                if (string.Equals(cert.Subject, cert.Issuer, StringComparison.Ordinal)) continue; // корень — только встроенный
                intermediates.Add(cert);
            }
        }

        using var chain = new X509Chain();
        chain.ChainPolicy.TrustMode = X509ChainTrustMode.CustomRootTrust;
        chain.ChainPolicy.RevocationMode = X509RevocationMode.NoCheck;
        chain.ChainPolicy.CustomTrustStore.AddRange(Roots);
        chain.ChainPolicy.ExtraStore.AddRange(intermediates);
        chain.ChainPolicy.ApplicationPolicy.Add(ClientAuth);
        chain.ChainPolicy.VerificationTime = now.UtcDateTime;
        chain.ChainPolicy.VerificationTimeIgnored = false;
        if (!chain.Build(leaf))
        {
            var reasons = string.Join(", ", chain.ChainStatus.Select(s => s.Status.ToString()));
            throw new CryptographicException("Issued certificate does not chain to the pinned root CA: " + reasons);
        }

        return intermediates;
    }

    public void Dispose()
    {
        foreach (var r in Roots) r.Dispose();
    }
}
