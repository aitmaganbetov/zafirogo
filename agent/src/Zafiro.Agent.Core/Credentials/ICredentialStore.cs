using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Zafiro.Agent.Local.V1;

namespace Zafiro.Agent.Core.Credentials;

/// <summary>Ключ ECDSA P-256 ПК. На Windows — именованный неэкспортируемый машинный ключ CNG.</summary>
public interface IAgentKey : IDisposable
{
    /// <summary>Имя ключа в хранилище (для удаления после продления).</summary>
    string Name { get; }

    /// <summary>Ключ для CSR и TLS.</summary>
    ECDsa Key { get; }
}

/// <summary>
/// Хранилище ключа и сертификата ПК (docs/agent-protocol.md «Клиент .NET»): Windows — CNG + DPAPI
/// (<see cref="WindowsCredentialStore"/>), Linux (тесты) — файл 0600 (<see cref="FileCredentialStore"/>).
/// </summary>
public interface ICredentialStore
{
    /// <summary>Новый ключ ECDSA P-256 (для Enroll или продления).</summary>
    IAgentKey CreateKey();

    /// <summary>Сохранённые данные; null — ПК не зарегистрирован или данные с другого ПК / повреждены.</summary>
    AgentCredentials? Load();

    /// <summary>
    /// Сохраняет новый сертификат с ключом <paramref name="key"/> (ключ переходит к результату). Если прежний сертификат был на другом ключе, его
    /// имя попадает в <see cref="StoredCredentials.PreviousKeyName"/> (удаляется <see cref="CommitRotation"/>).
    /// </summary>
    AgentCredentials Save(StoredCredentials stored, IAgentKey key, X509Certificate2Collection intermediates);

    /// <summary>Удаляет прежний ключ после первого успешного подключения с новым сертификатом.</summary>
    void CommitRotation();

    /// <summary>Удаляет ключ (неудачная регистрация).</summary>
    void DeleteKey(IAgentKey key);

    /// <summary>Удаляет ключ и сертификат (CERTIFICATE_REVOKED): нужна новая регистрация.</summary>
    void Clear();
}

/// <summary>Сертификат ПК с закрытым ключом, промежуточные CA и метаданные регистрации.</summary>
public sealed class AgentCredentials : IDisposable
{
    private readonly IDisposable? _key;

    public AgentCredentials(X509Certificate2 certificate, X509Certificate2Collection intermediates, StoredCredentials stored, IDisposable? key = null)
    {
        _key = key;
        Certificate = certificate;
        Intermediates = intermediates;
        Stored = stored;
    }

    /// <summary>Сертификат с закрытым ключом (для TLS).</summary>
    public X509Certificate2 Certificate { get; }

    /// <summary>Промежуточные CA (отправляются вместе с сертификатом).</summary>
    public X509Certificate2Collection Intermediates { get; }

    /// <summary>Метаданные (computer_id, club_id, адрес, сроки).</summary>
    public StoredCredentials Stored { get; }

    public DateTimeOffset NotAfter => Stored.NotAfter?.ToDateTimeOffset() ?? new DateTimeOffset(Certificate.NotAfter.ToUniversalTime());

    public DateTimeOffset RenewAfter => Stored.RenewAfter?.ToDateTimeOffset() ?? NotAfter - (NotAfter - new DateTimeOffset(Certificate.NotBefore.ToUniversalTime())) / 3;

    /// <summary>Сертификат с закрытым ключом из PEM и ключа. Ключ принадлежит результату (освобождается в <see cref="Dispose"/>).</summary>
    public static AgentCredentials Create(StoredCredentials stored, ECDsa key, X509Certificate2Collection intermediates)
    {
        ArgumentNullException.ThrowIfNull(stored);
        ArgumentNullException.ThrowIfNull(key);
        using var pub = X509Certificate2.CreateFromPem(stored.CertificatePem);
        var withKey = pub.CopyWithPrivateKey(key);
        return new AgentCredentials(withKey, intermediates, stored, key);
    }

    /// <summary>Промежуточные CA из сохранённой цепочки (корни пропускаются — они встроены).</summary>
    public static X509Certificate2Collection IntermediatesFrom(IEnumerable<string> chainPem)
    {
        var result = new X509Certificate2Collection();
        foreach (var pem in chainPem)
        {
            var c = new X509Certificate2Collection();
            c.ImportFromPem(pem);
            foreach (var cert in c)
            {
                if (!string.Equals(cert.Subject, cert.Issuer, StringComparison.Ordinal)) result.Add(cert);
            }
        }

        return result;
    }

    public void Dispose()
    {
        Certificate.Dispose();
        foreach (var c in Intermediates) c.Dispose();
        _key?.Dispose();
    }
}
