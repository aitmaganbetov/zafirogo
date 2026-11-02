using Google.Protobuf;
using Grpc.Core;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Guest;

/// <summary>
/// Картинки клуба для UI (логотип, картинка экрана входа, новости; AG-13). У процесса UI нет сети: служба берёт
/// картинку у сервера (AgentService.GetImage, клуб — по сертификату ПК) и кэширует на диске
/// (<c>&lt;DataDirectory&gt;/images</c>, не больше <see cref="MaxCacheBytes"/>, старые удаляются первыми). Свежая
/// (проверена за <see cref="FreshFor"/>) отдаётся без сервера; иначе — запрос с etag (not_modified — из кэша);
/// сервер недоступен — из кэша; картинки больше нет (NOT_FOUND) — удаляется из кэша. Принимаются только PNG и JPEG
/// до 3 МиБ (как старый агент: <c>img-src</c> только сайта Zafiro).
/// </summary>
public sealed class ImageCache
{
    /// <summary>Предел одной картинки (club_images: картинка входа ≤ 3 МБ).</summary>
    public const int MaxImageBytes = 3 * 1024 * 1024;

    /// <summary>Предел кэша на диске.</summary>
    public const long MaxCacheBytes = 32L * 1024 * 1024;

    /// <summary>Сколько картинка считается свежей без проверки на сервере.</summary>
    public static readonly TimeSpan FreshFor = TimeSpan.FromMinutes(10);

    private readonly string? _dir;
    private readonly Func<GetImageRequest, CancellationToken, Task<GetImageResponse>> _fetch;
    private readonly TimeProvider _time;
    private readonly Dictionary<string, DateTimeOffset> _checked = new(StringComparer.Ordinal);
    private readonly object _gate = new();

    /// <param name="directory">Каталог кэша; null — без диска (только сервер).</param>
    public ImageCache(string? directory, Func<GetImageRequest, CancellationToken, Task<GetImageResponse>> fetch, TimeProvider? time = null)
    {
        _dir = string.IsNullOrEmpty(directory) ? null : directory;
        _fetch = fetch ?? throw new ArgumentNullException(nameof(fetch));
        _time = time ?? TimeProvider.System;
    }

    /// <summary>Картинка (data, content_type, etag). Неверный запрос — <see cref="ArgumentException"/>; нет картинки — ошибка сервера.</summary>
    public async Task<GetImageResponse> GetAsync(GetImageRequest request, CancellationToken ct)
    {
        ArgumentNullException.ThrowIfNull(request);
        var key = Key(request);
        var cached = Load(key);
        lock (_gate)
        {
            if (cached is not null && _checked.TryGetValue(key, out var at) && _time.GetUtcNow() - at < FreshFor) return cached;
        }

        GetImageResponse fresh;
        try
        {
            fresh = await _fetch(new GetImageRequest { Kind = request.Kind, Id = request.Id, IfNoneMatch = cached?.Etag ?? "" }, ct).ConfigureAwait(false);
        }
        catch (AgentRpcException e) when (e.StatusCode == StatusCode.NotFound)
        {
            Remove(key);
            throw;
        }
        catch (AgentRpcException) when (cached is not null && !ct.IsCancellationRequested)
        {
            return cached; // сервер недоступен — последняя известная картинка
        }

        if (fresh.NotModified && cached is not null)
        {
            Touch(key);
            return cached;
        }

        if (!Valid(fresh))
            throw new AgentRpcException(StatusCode.InvalidArgument, ErrorCode.ValidationFailed, null, "server image is not a PNG/JPEG up to 3 MiB",
                null, new Dictionary<string, string>(), [], null);
        var entry = new GetImageResponse { Data = fresh.Data, ContentType = fresh.ContentType, Etag = fresh.Etag };
        Save(key, entry);
        Touch(key);
        return entry;
    }

    /// <summary>Имя файла кэша: вид и id (без строк из запроса — не путь).</summary>
    public static string Key(GetImageRequest request)
    {
        ArgumentNullException.ThrowIfNull(request);
        return request.Kind switch
        {
            ImageKind.ClubLogo when request.Id == 0 => "logo",
            ImageKind.Signin when request.Id == 0 => "signin",
            ImageKind.News when request.Id > 0 => "news-" + request.Id.ToString(System.Globalization.CultureInfo.InvariantCulture),
            _ => throw new ArgumentException("Unknown image.", nameof(request)),
        };
    }

    private static bool Valid(GetImageResponse r) =>
        r.Data.Length is > 0 and <= MaxImageBytes && r.ContentType is "image/png" or "image/jpeg";

    private string? PathOf(string key) => _dir is null ? null : Path.Combine(_dir, key + ".pb");

    private GetImageResponse? Load(string key)
    {
        var path = PathOf(key);
        if (path is null) return null;
        try
        {
            var bytes = AtomicFile.Read(path);
            if (bytes is null) return null;
            var r = GetImageResponse.Parser.ParseFrom(bytes);
            return Valid(r) ? r : null;
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException or Google.Protobuf.InvalidProtocolBufferException)
        {
            return null;
        }
    }

    private void Save(string key, GetImageResponse entry)
    {
        var path = PathOf(key);
        if (path is null) return;
        try
        {
            AtomicFile.EnsureDirectory(_dir!);
            AtomicFile.Write(path, entry.ToByteArray());
            Evict(path);
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException)
        {
            // Кэш — не обязателен: картинка отдана, на диск не легла.
        }
    }

    /// <summary>Кэш больше предела — удаляются самые старые файлы (кроме только что записанного).</summary>
    private void Evict(string keep)
    {
        var files = new DirectoryInfo(_dir!).GetFiles("*.pb").OrderByDescending(f => f.LastWriteTimeUtc).ToList();
        long total = 0;
        foreach (var f in files)
        {
            total += f.Length;
            if (total > MaxCacheBytes && !string.Equals(f.FullName, Path.GetFullPath(keep), StringComparison.Ordinal)) f.Delete();
        }
    }

    private void Touch(string key)
    {
        lock (_gate) _checked[key] = _time.GetUtcNow();
    }

    private void Remove(string key)
    {
        lock (_gate) _checked.Remove(key);
        var path = PathOf(key);
        if (path is not null) AtomicFile.Delete(path);
    }
}
