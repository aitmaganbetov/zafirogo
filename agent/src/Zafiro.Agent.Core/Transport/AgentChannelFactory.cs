using System.Net.Security;
using System.Security.Authentication;
using Grpc.Core;
using Grpc.Core.Interceptors;
using Grpc.Net.Client;
using Zafiro.Agent.Core.Credentials;

namespace Zafiro.Agent.Core.Transport;

/// <summary>Канал gRPC и invoker с обязательными метаданными (agent.proto, шапка п. 2).</summary>
public sealed class AgentChannel : IDisposable
{
    internal AgentChannel(GrpcChannel channel, CallInvoker invoker, string endpoint)
    {
        Channel = channel;
        Invoker = invoker;
        Endpoint = endpoint;
    }

    public GrpcChannel Channel { get; }

    /// <summary>Invoker с заголовками zafiro-protocol-version, zafiro-agent-version, zafiro-locale.</summary>
    public CallInvoker Invoker { get; }

    /// <summary>"host:port".</summary>
    public string Endpoint { get; }

    public void Dispose() => Channel.Dispose();
}

/// <summary>
/// Каналы gRPC агента (docs/agent-protocol.md §12.6 п. 5, §12.4): <see cref="SocketsHttpHandler"/> с TLS 1.2+,
/// проверкой сервера только по встроенному корню (pinning), клиентским сертификатом ПК (mTLS), HTTP/2 PING каждые 30 с
/// (таймаут 10 с; сервер разрешает не чаще 20 с), одно долгое соединение без закрытия по простою. Лимиты сообщений:
/// отправка ≤ 1 МиБ (agent.proto п. 9), приём ≤ 8 МиБ (MaxSendMsgSize сервера — словарь переводов).
/// </summary>
public sealed class AgentChannelFactory
{
    /// <summary>Текущая версия протокола внутри v1 (Hello.protocol_version, заголовок zafiro-protocol-version).</summary>
    public const uint ProtocolVersion = 1;

    public const int MaxSendMessageSize = 1024 * 1024;
    public const int MaxReceiveMessageSize = 8 * 1024 * 1024;
    public static readonly TimeSpan KeepAlivePingDelay = TimeSpan.FromSeconds(30);
    public static readonly TimeSpan KeepAlivePingTimeout = TimeSpan.FromSeconds(10);
    public static readonly TimeSpan ConnectTimeout = TimeSpan.FromSeconds(10);

    private readonly AgentOptions _options;
    private readonly TrustAnchors _trust;

    public AgentChannelFactory(AgentOptions options, TrustAnchors trust)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        _trust = trust ?? throw new ArgumentNullException(nameof(trust));
    }

    /// <summary>Канал регистрации: TLS без клиентского сертификата (с просроченным сертификатом TLS не поднимется).</summary>
    public AgentChannel CreateEnrollChannel() => Create(_options.EnrollEndpoint, null);

    /// <summary>Канал mTLS к AgentService (адрес — из ответа Enroll).</summary>
    public AgentChannel CreateChannel(AgentCredentials credentials)
    {
        ArgumentNullException.ThrowIfNull(credentials);
        var endpoint = string.IsNullOrEmpty(credentials.Stored.AgentEndpoint) ? _options.EnrollEndpoint : credentials.Stored.AgentEndpoint;
        return Create(endpoint, credentials);
    }

    /// <summary>Обработчик HTTP (открыт для тестов).</summary>
    public SocketsHttpHandler CreateHandler(AgentCredentials? credentials)
    {
        var ssl = new SslClientAuthenticationOptions
        {
            EnabledSslProtocols = SslProtocols.Tls12 | SslProtocols.Tls13,
            CertificateRevocationCheckMode = System.Security.Cryptography.X509Certificates.X509RevocationMode.NoCheck,
            CertificateChainPolicy = _trust.ServerPolicy(),
        };
        if (credentials is not null)
        {
            // Сертификат ПК + промежуточные CA одной цепочкой; offline — без загрузки AIA.
            ssl.ClientCertificateContext = SslStreamCertificateContext.Create(credentials.Certificate, credentials.Intermediates, offline: true);
        }

        return new SocketsHttpHandler
        {
            SslOptions = ssl,
            ConnectTimeout = ConnectTimeout,
            KeepAlivePingDelay = KeepAlivePingDelay,
            KeepAlivePingTimeout = KeepAlivePingTimeout,
            KeepAlivePingPolicy = HttpKeepAlivePingPolicy.Always,
            PooledConnectionIdleTimeout = Timeout.InfiniteTimeSpan,
            PooledConnectionLifetime = Timeout.InfiniteTimeSpan,
            EnableMultipleHttp2Connections = false,
            AutomaticDecompression = System.Net.DecompressionMethods.None,
            UseProxy = false,
            AllowAutoRedirect = false,
        };
    }

    private AgentChannel Create(string endpoint, AgentCredentials? credentials)
    {
        var address = ToAddress(endpoint);
        var channel = GrpcChannel.ForAddress(address, new GrpcChannelOptions
        {
            HttpHandler = CreateHandler(credentials),
            DisposeHttpClient = true,
            MaxSendMessageSize = MaxSendMessageSize,
            MaxReceiveMessageSize = MaxReceiveMessageSize,
            // Повторы — свои (идемпотентность денежных RPC по ключу, backoff стрима), не встроенные политики.
            MaxRetryAttempts = 0,
        });
        var invoker = channel.Intercept(AddMetadata);
        return new AgentChannel(channel, invoker, endpoint);
    }

    private Metadata AddMetadata(Metadata md)
    {
        md.Add("zafiro-protocol-version", ProtocolVersion.ToString(System.Globalization.CultureInfo.InvariantCulture));
        md.Add("zafiro-agent-version", _options.AgentVersion);
        md.Add("zafiro-locale", _options.Locale);
        return md;
    }

    /// <summary>"host:port" → https://host:port (только https: агент без TLS не работает).</summary>
    public static Uri ToAddress(string endpoint)
    {
        ArgumentException.ThrowIfNullOrEmpty(endpoint);
        if (endpoint.Contains("://", StringComparison.Ordinal))
        {
            var u = new Uri(endpoint);
            if (u.Scheme != Uri.UriSchemeHttps) throw new ArgumentException("Agent endpoint must use https.", nameof(endpoint));
            return u;
        }

        return new Uri("https://" + endpoint);
    }
}
