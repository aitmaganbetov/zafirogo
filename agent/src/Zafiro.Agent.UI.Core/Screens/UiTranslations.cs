using Google.Protobuf;
using Zafiro.Agent.UI.Ipc;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.UI.Screens;

/// <summary>
/// Тексты окон UI без WebView2 (уведомления в углу экрана): переводы с сервера через службу (agent.GetTranslations),
/// ключ — русский текст; нет перевода — показывается ключ. Подстановки <c>:name</c> — как <c>fill()</c> страницы
/// (длинные имена раньше коротких: <c>:count</c> не задевает <c>:countX</c>).
/// </summary>
public sealed class UiTranslations
{
    private readonly IIpcTransport _ipc;
    private readonly SemaphoreSlim _gate = new(1, 1);
    private volatile IReadOnlyDictionary<string, string> _lines = new Dictionary<string, string>(StringComparer.Ordinal);
    private string _locale = "ru";
    private long _version;

    public UiTranslations(IIpcTransport ipc)
    {
        _ipc = ipc ?? throw new ArgumentNullException(nameof(ipc));
    }

    public string Locale => _locale;

    public string T(string key, params (string Name, string Value)[] parameters)
    {
        ArgumentNullException.ThrowIfNull(key);
        var text = _lines.TryGetValue(key, out var line) && !string.IsNullOrEmpty(line) ? line : key;
        foreach (var (name, value) in parameters.OrderByDescending(p => p.Name.Length))
            text = text.Replace(":" + name, value, StringComparison.Ordinal);
        return text;
    }

    /// <summary>Перечитать переводы, если сменился язык (null — прежний) или версия; ошибка — остаются прежние.</summary>
    public async Task RefreshAsync(string? locale, long version, CancellationToken cancellationToken)
    {
        var target = string.IsNullOrEmpty(locale) ? _locale : locale;
        if (target == _locale && version == _version && _lines.Count > 0) return;
        if (!await _gate.WaitAsync(0, cancellationToken).ConfigureAwait(false)) return;
        try
        {
            var request = new GetTranslationsRequest { Locale = target, KnownVersion = target == _locale ? _version : 0 };
            var response = await _ipc.CallAsync("agent.GetTranslations", request.ToByteString(), cancellationToken).ConfigureAwait(false);
            if (response.Error is not null) return;
            var result = GetTranslationsResponse.Parser.ParseFrom(response.Payload);
            if (!result.NotModified) _lines = new Dictionary<string, string>(result.Lines, StringComparer.Ordinal);
            _locale = target;
            _version = result.Version;
        }
        catch (Exception e) when (e is IOException or TimeoutException or InvalidProtocolBufferException)
        {
        }
        finally
        {
            _gate.Release();
        }
    }
}
