using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Transport;
using Zafiro.Agent.Local.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Enrollment;

/// <summary>Чем регистрируется ПК: одноразовый код из панели или ключ образа клуба (бездисковые ПК, Q-037).</summary>
public sealed record EnrollSecret
{
    private EnrollSecret(string? code, string? imageKey)
    {
        Code = code;
        ImageKey = imageKey;
    }

    public string? Code { get; }

    public string? ImageKey { get; }

    /// <summary>Код вводится как "ABCD-EFGH": дефисы и пробелы убираются, регистр — верхний.</summary>
    public static EnrollSecret FromCode(string code)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(code);
        var normalized = new string(code.Where(char.IsAsciiLetterOrDigit).ToArray()).ToUpperInvariant();
        return new EnrollSecret(normalized, null);
    }

    public static EnrollSecret FromImageKey(string imageKey)
    {
        ArgumentException.ThrowIfNullOrWhiteSpace(imageKey);
        return new EnrollSecret(null, imageKey.Trim());
    }

    // Секрет не попадает в журналы через ToString записи.
    public override string ToString() => Code is not null ? "EnrollSecret(code)" : "EnrollSecret(image key)";
}

/// <summary>
/// Регистрация ПК и продление сертификата (docs/agent-protocol.md §3, §12.5–12.8):
/// новый ключ ECDSA P-256 в хранилище → CSR (<see cref="CertificateRequest"/>, SHA-256) → Enroll по отдельному
/// каналу без клиентского сертификата → проверка ответа (цепочка до встроенного корня, ключ — наш) → сохранение.
/// Бездисковый ПК с новым MAC получает PENDING_APPROVAL + RetryInfo и повторяет Enroll с тем же ключом, пока сотрудник
/// не подтвердит ПК в панели. Продление — RenewCertificate по текущему mTLS-каналу с НОВЫМ ключом.
/// </summary>
public sealed class Enroller
{
    /// <summary>Пауза между попытками при PENDING_APPROVAL, если сервер не прислал RetryInfo (сервер шлёт 30 с).</summary>
    public static readonly TimeSpan DefaultPendingRetry = TimeSpan.FromSeconds(30);

    private readonly AgentChannelFactory _channels;
    private readonly TrustAnchors _trust;
    private readonly ICredentialStore _store;
    private readonly TimeProvider _time;
    private readonly AgentOptions _options;
    private readonly ILogger _log;

    public Enroller(AgentOptions options, AgentChannelFactory channels, TrustAnchors trust, ICredentialStore store, TimeProvider time, ILogger log)
    {
        _options = options;
        _channels = channels;
        _trust = trust;
        _store = store;
        _time = time;
        _log = log;
    }

    /// <summary>CSR в PEM для ключа (Subject любой — сервер его заменяет).</summary>
    public static string CreateCsr(ECDsa key)
    {
        var req = new CertificateRequest("CN=zafiro-agent", key, HashAlgorithmName.SHA256);
        return req.CreateSigningRequestPem();
    }

    /// <summary>
    /// Регистрация. <paramref name="onPending"/> вызывается при PENDING_APPROVAL (ожидание подтверждения) с задержкой
    /// до следующей попытки. Ошибки — типизированные <see cref="AgentRpcException"/>; ключ при неудаче удаляется.
    /// </summary>
    public async Task<AgentCredentials> EnrollAsync(EnrollSecret secret, DeviceInfo device, Action<TimeSpan>? onPending, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(secret);
        ArgumentNullException.ThrowIfNull(device);
        var key = _store.CreateKey();
        var saved = false;
        try
        {
            var request = new EnrollRequest
            {
                CsrPem = CreateCsr(key.Key),
                Device = device,
            };
            if (secret.Code is not null) request.EnrollmentCode = secret.Code;
            else request.ImageKey = secret.ImageKey!;

            using var channel = _channels.CreateEnrollChannel();
            var client = new EnrollmentService.EnrollmentServiceClient(channel.Invoker);
            while (true)
            {
                try
                {
                    var response = await RpcErrors.Call(() => client.EnrollAsync(request,
                        deadline: _time.GetUtcNow().UtcDateTime + _options.WriteDeadline, cancellationToken: cancellationToken).ResponseAsync).ConfigureAwait(false);
                    var creds = Accept(response.CertificatePem, response.CaChainPem, key, new StoredCredentials
                    {
                        ComputerId = response.ComputerId,
                        ClubId = response.ClubId,
                        ClubName = response.ClubName,
                        ComputerNumber = response.ComputerNumber,
                        AgentEndpoint = response.AgentEndpoint,
                        NotAfter = response.NotAfter,
                        RenewAfter = response.RenewAfter,
                        Diskless = secret.ImageKey is not null,
                    });
                    saved = true;
                    _log.LogInformation("enrolled: computer {ComputerId}, club {ClubId}, endpoint {Endpoint}", response.ComputerId, response.ClubId, response.AgentEndpoint);
                    return creds;
                }
                catch (PendingApprovalException e)
                {
                    var delay = e.RetryAfter is { } ra && ra > TimeSpan.Zero ? ra : DefaultPendingRetry;
                    _log.LogInformation("enroll: waiting for approval in the club panel, retry in {Delay}", delay);
                    onPending?.Invoke(delay);
                    await Task.Delay(delay, _time, cancellationToken).ConfigureAwait(false);
                }
            }
        }
        finally
        {
            if (!saved) _store.DeleteKey(key);
        }
    }

    /// <summary>
    /// Продление: новый ключ → CSR → RenewCertificate по текущему mTLS-каналу → новый сертификат сохраняется, прежний
    /// ключ удаляется после первого успешного подключения с новым (<see cref="ICredentialStore.CommitRotation"/>).
    /// </summary>
    public async Task<AgentCredentials> RenewAsync(AgentChannel channel, AgentCredentials current, DeviceInfo device, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(channel);
        ArgumentNullException.ThrowIfNull(current);
        var key = _store.CreateKey();
        var saved = false;
        try
        {
            var client = new EnrollmentService.EnrollmentServiceClient(channel.Invoker);
            var response = await RpcErrors.Call(() => client.RenewCertificateAsync(new RenewCertificateRequest { CsrPem = CreateCsr(key.Key), Device = device },
                deadline: _time.GetUtcNow().UtcDateTime + _options.WriteDeadline, cancellationToken: cancellationToken).ResponseAsync).ConfigureAwait(false);
            var meta = current.Stored.Clone();
            meta.CertificatePem = "";
            meta.CaChainPem.Clear();
            meta.NotAfter = response.NotAfter;
            meta.RenewAfter = response.RenewAfter;
            var creds = Accept(response.CertificatePem, response.CaChainPem, key, meta);
            saved = true;
            _log.LogInformation("certificate renewed, valid until {NotAfter}", creds.NotAfter);
            return creds;
        }
        finally
        {
            if (!saved) _store.DeleteKey(key);
        }
    }

    private AgentCredentials Accept(string certificatePem, IEnumerable<string> chainPem, IAgentKey key, StoredCredentials meta)
    {
        var chain = chainPem.ToList();
        X509Certificate2Collection intermediates;
        using (var leaf = X509Certificate2.CreateFromPem(certificatePem))
        {
            intermediates = _trust.ValidateIssued(leaf, chain, key.Key, _time.GetUtcNow());
        }

        meta.CertificatePem = certificatePem;
        meta.CaChainPem.Clear();
        meta.CaChainPem.AddRange(chain);
        return _store.Save(meta, key, intermediates);
    }
}
