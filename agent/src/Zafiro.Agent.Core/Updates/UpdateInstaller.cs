using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Updates;

/// <summary>
/// Скачивание и установка обновления после проверки подписи (docs/agent-protocol.md §6 п. 3): только https и хосты из
/// списка, размер не больше size_bytes, затем SHA-256 по открытому файлу (пока он открыт, подменить его нельзя) и запуск
/// установщика платформой. Любое несовпадение — файл удаляется, итог UPDATE_VERIFICATION_FAILED.
/// Перенесено из старого агента с добавлением Ed25519: WindowsAgent/ZafiroAgent/Service/AgentUpdater.cs.
/// </summary>
public sealed class UpdateInstaller
{
    /// <summary>Предел размера установщика (как MaxBytes старого AgentUpdater).</summary>
    public const long MaxBytes = 300L * 1024 * 1024;

    private readonly IPlatform _platform;
    private readonly HttpClient _http;
    private readonly IReadOnlyCollection<string> _allowedHosts;
    private readonly ILogger _log;

    public UpdateInstaller(IPlatform platform, HttpClient http, IReadOnlyCollection<string> allowedHosts, ILogger log)
    {
        _platform = platform;
        _http = http;
        _allowedHosts = allowedHosts;
        _log = log;
    }

    /// <summary>HTTP-клиент загрузки (редиректы GitHub на хранилище — до 5).</summary>
    public static HttpClient CreateHttpClient() =>
        new(new SocketsHttpHandler { AllowAutoRedirect = true, MaxAutomaticRedirections = 5 }) { Timeout = TimeSpan.FromMinutes(20) };

    /// <summary>Скачивает, проверяет и запускает установщик. Подпись уже проверена <see cref="UpdateVerifier.Verify"/>.</summary>
    public async Task<(bool Ok, ErrorCode Error)> DownloadAndInstallAsync(UpdateAvailable u, CancellationToken cancellationToken)
    {
        ArgumentNullException.ThrowIfNull(u);
        if (u.SizeBytes > MaxBytes) return (false, ErrorCode.UpdateVerificationFailed);
        Directory.CreateDirectory(_platform.UpdateDirectory);
        var safeVersion = new string(u.Version.Where(c => char.IsAsciiLetterOrDigit(c) || c is '.' or '-').ToArray());
        var path = Path.Combine(_platform.UpdateDirectory, "ZafiroAgentSetup-" + safeVersion + (OperatingSystem.IsWindows() ? ".exe" : ".bin"));
        try
        {
            using (var response = await _http.GetAsync(new Uri(u.Url), HttpCompletionOption.ResponseHeadersRead, cancellationToken).ConfigureAwait(false))
            {
                var final = response.RequestMessage?.RequestUri ?? new Uri(u.Url);
                if (!response.IsSuccessStatusCode || final.Scheme != Uri.UriSchemeHttps
                    || (_allowedHosts.Count > 0 && !_allowedHosts.Contains(final.Host, StringComparer.OrdinalIgnoreCase))
                    || response.Content.Headers.ContentLength > u.SizeBytes)
                {
                    _log.LogWarning("update {Version}: download refused (HTTP {Status} from {Host})", u.Version, (int)response.StatusCode, final.Host);
                    return (false, ErrorCode.UpdateVerificationFailed);
                }

                var source = await response.Content.ReadAsStreamAsync(cancellationToken).ConfigureAwait(false);
                await using (source.ConfigureAwait(false))
                {
                    var target = new FileStream(path, FileMode.Create, FileAccess.Write, FileShare.None);
                    await using (target.ConfigureAwait(false))
                    {
                        var buffer = new byte[81920];
                        long total = 0;
                        int read;
                        while ((read = await source.ReadAsync(buffer, cancellationToken).ConfigureAwait(false)) > 0)
                        {
                            total += read;
                            if (total > u.SizeBytes) throw new IOException("installer is larger than size_bytes");
                            await target.WriteAsync(buffer.AsMemory(0, read), cancellationToken).ConfigureAwait(false);
                        }
                    }
                }
            }

            var file = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read);
            await using (file.ConfigureAwait(false))
            {
                if (!await UpdateVerifier.VerifyFileAsync(file, u, cancellationToken).ConfigureAwait(false))
                {
                    _log.LogWarning("update {Version}: size or sha256 mismatch, installer removed", u.Version);
                    await file.DisposeAsync().ConfigureAwait(false);
                    TryDelete(path);
                    return (false, ErrorCode.UpdateVerificationFailed);
                }

                var started = await _platform.InstallUpdateAsync(path, cancellationToken).ConfigureAwait(false);
                return started ? (true, ErrorCode.Unspecified) : (false, ErrorCode.Internal);
            }
        }
        catch (Exception ex) when (ex is HttpRequestException or IOException or UnauthorizedAccessException
                                   || (ex is OperationCanceledException && !cancellationToken.IsCancellationRequested))
        {
            _log.LogWarning("update {Version}: download failed ({Error})", u.Version, ex.GetType().Name);
            TryDelete(path);
            return (false, ex is IOException ? ErrorCode.UpdateVerificationFailed : ErrorCode.Internal);
        }
    }

    private static void TryDelete(string path)
    {
        try
        {
            File.Delete(path);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
        }
    }
}
