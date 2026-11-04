using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using Google.Protobuf;
using Zafiro.Agent.Ipc;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.UI.Ipc;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.UI.Bridge;

/// <summary>Действия процесса UI, которые страница может попросить (не через службу).</summary>
public interface IUiHost
{
    /// <summary>«Вернуться к игре»: скрыть лаунчер (только когда экран не нужен — идёт сессия).</summary>
    void ReturnToDesktop();

    /// <summary>Выключить (false) или перезагрузить (true) ПК с экрана входа. false — Windows не дал.</summary>
    bool Power(bool restart);

    /// <summary>Раскладка клавиатуры для поля пароля (next — переключить на следующую, как Alt+Shift).</summary>
    string InputLanguage(bool next);
}

/// <summary>
/// Мост страницы WebView2 ↔ служба. Страница шлёт <c>{"id": n, "method": "...", "params": {...}}</c>
/// (window.chrome.webview.postMessage) и получает <c>{"id": n, "result": {...}}</c> или <c>{"id": n, "error": {...}}</c>;
/// события службы — <c>{"event": {...}}</c>. Принимаются только методы <see cref="IpcMethods"/> (их запросы
/// разбираются строго по схеме proto: неизвестное поле, неверный тип — отказ) и локальные методы <c>ui.*</c> со своей
/// схемой. Сообщение больше <see cref="MaxMessageBytes"/>, лишние ключи, нецелый id — отказ без вызова службы.
/// JSON — каноническое отображение proto3: имена lowerCamelCase, int64 (тиыны) — строкой с целым числом, enum — именем.
/// </summary>
public sealed partial class BridgeRouter
{
    /// <summary>Наибольшее сообщение страницы (байты UTF-8).</summary>
    public const int MaxMessageBytes = 64 * 1024;

    /// <summary>Наибольший id запроса (Number.MAX_SAFE_INTEGER).</summary>
    public const long MaxId = 9007199254740991;

    private static readonly JsonParser Parser = new(JsonParser.Settings.Default.WithIgnoreUnknownFields(false).WithRecursionLimit(16));
    private static readonly JsonFormatter Formatter = new(JsonFormatter.Settings.Default.WithFormatDefaultValues(true));

    private readonly IIpcTransport _ipc;
    private readonly IUiHost _host;

    public BridgeRouter(IIpcTransport ipc, IUiHost host)
    {
        _ipc = ipc ?? throw new ArgumentNullException(nameof(ipc));
        _host = host ?? throw new ArgumentNullException(nameof(host));
    }

    /// <summary>Локальные методы процесса UI (не уходят в службу).</summary>
    public static IReadOnlyCollection<string> LocalMethods { get; } = ["ui.ReturnToDesktop", "ui.Power", "ui.InputLanguage", "ui.QrImage"];

    [GeneratedRegex("^(agent|ui)\\.[A-Za-z]{1,40}$|^[A-Za-z]{1,32}/[A-Za-z]{1,40}$", RegexOptions.CultureInvariant)]
    private static partial Regex MethodName();

    /// <summary>
    /// Обрабатывает сообщение страницы. Возвращает JSON ответа или null — сообщение отброшено целиком (не разобрать id).
    /// </summary>
    public async Task<string?> HandleAsync(string json, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(json) || Encoding.UTF8.GetByteCount(json) > MaxMessageBytes) return null;
        if (!TryParseEnvelope(json, out var id, out var method, out var parameters)) return null;
        try
        {
            if (method.StartsWith("ui.", StringComparison.Ordinal))
                return Result(id, LocalCall(method, parameters));
            // Картинки — только через виртуальный хост (ImageRoute), не JSON-мостом.
            if (method == "agent.GetImage" || !IpcMethods.All.TryGetValue(method, out var ipcMethod))
                return Failure(id, BridgeError(Grpc.Core.StatusCode.Unimplemented));

            IMessage request;
            try
            {
                request = Parser.Parse(parameters ?? "{}", ipcMethod.Request);
            }
            catch (Exception e) when (e is InvalidProtocolBufferException or InvalidJsonException or FormatException or OverflowException or ArgumentException)
            {
                return Failure(id, BridgeError(Grpc.Core.StatusCode.InvalidArgument));
            }

            Response response;
            try
            {
                response = await _ipc.CallAsync(method, request.ToByteString(), cancellationToken, TimeoutOf(method)).ConfigureAwait(false);
            }
            catch (Exception e) when (e is IOException or TimeoutException)
            {
                return Failure(id, BridgeError(Grpc.Core.StatusCode.Unavailable));
            }

            if (response.Error is { } error) return Failure(id, error);
            var result = ipcMethod.Response.Parser.ParseFrom(response.Payload);
            return Result(id, Formatter.Format(result));
        }
        catch (BridgeValidationException)
        {
            return Failure(id, BridgeError(Grpc.Core.StatusCode.InvalidArgument));
        }
    }

    /// <summary>Сколько ждать ответа службы: ожидание QR-входа — до 10 минут (срок QR-кода), прочее — по умолчанию.</summary>
    public static TimeSpan? TimeoutOf(string method) => method == "GuestAuthService/WatchQrLogin" ? TimeSpan.FromMinutes(11) : null;

    /// <summary>Событие службы для страницы.</summary>
    public static string EventJson(Event e)
    {
        ArgumentNullException.ThrowIfNull(e);
        return "{\"event\":" + Formatter.Format(e) + "}";
    }

    private static bool TryParseEnvelope(string json, out long id, out string method, out string? parameters)
    {
        id = 0;
        method = "";
        parameters = null;
        try
        {
            using var doc = JsonDocument.Parse(json, new JsonDocumentOptions { MaxDepth = 32 });
            var root = doc.RootElement;
            if (root.ValueKind != JsonValueKind.Object) return false;
            var seen = 0;
            foreach (var p in root.EnumerateObject())
            {
                switch (p.Name)
                {
                    case "id" when p.Value.ValueKind == JsonValueKind.Number && p.Value.TryGetInt64(out var v) && v > 0 && v <= MaxId:
                        id = v;
                        break;
                    case "method" when p.Value.ValueKind == JsonValueKind.String:
                        method = p.Value.GetString() ?? "";
                        break;
                    case "params" when p.Value.ValueKind == JsonValueKind.Object:
                        parameters = p.Value.GetRawText();
                        break;
                    default:
                        return false;
                }

                seen++;
            }

            return seen >= 2 && id > 0 && MethodName().IsMatch(method);
        }
        catch (JsonException)
        {
            return false;
        }
    }

    private string LocalCall(string method, string? parameters)
    {
        using var doc = JsonDocument.Parse(parameters ?? "{}");
        var p = doc.RootElement;
        switch (method)
        {
            case "ui.ReturnToDesktop":
                Expect(p);
                _host.ReturnToDesktop();
                return "{}";
            case "ui.Power":
                Expect(p, "action");
                var action = String(p, "action", 16);
                if (action is not ("restart" or "shutdown")) throw new BridgeValidationException();
                return "{\"ok\":" + (_host.Power(action == "restart") ? "true" : "false") + "}";
            case "ui.InputLanguage":
                Expect(p, "next");
                var next = p.TryGetProperty("next", out var n) && n.ValueKind switch
                {
                    JsonValueKind.True => true,
                    JsonValueKind.False => false,
                    _ => throw new BridgeValidationException(),
                };
                return JsonSerializer.Serialize(new LabelResult(_host.InputLanguage(next)), BridgeJson.Default.LabelResult);
            case "ui.QrImage":
                Expect(p, "text");
                var text = String(p, "text", QrCodes.MaxTextLength);
                if (!QrCodes.IsAllowed(text)) throw new BridgeValidationException();
                return JsonSerializer.Serialize(new DataUrlResult(QrCodes.ToPngDataUrl(text)), BridgeJson.Default.DataUrlResult);
            default:
                throw new BridgeValidationException();
        }
    }

    // Только перечисленные ключи (все необязательны, кроме проверяемых отдельно).
    private static void Expect(JsonElement p, params string[] allowed)
    {
        if (p.ValueKind != JsonValueKind.Object) throw new BridgeValidationException();
        foreach (var prop in p.EnumerateObject())
            if (Array.IndexOf(allowed, prop.Name) < 0) throw new BridgeValidationException();
    }

    private static string String(JsonElement p, string name, int maxLength)
    {
        if (!p.TryGetProperty(name, out var v) || v.ValueKind != JsonValueKind.String) throw new BridgeValidationException();
        var s = v.GetString() ?? "";
        if (s.Length == 0 || s.Length > maxLength) throw new BridgeValidationException();
        return s;
    }

    private static Error BridgeError(Grpc.Core.StatusCode status) => new()
    {
        ErrorCode = status == Grpc.Core.StatusCode.InvalidArgument ? ErrorCode.ValidationFailed : ErrorCode.Unspecified,
        GrpcStatus = (int)status,
    };

    private static string Result(long id, string resultJson) => "{\"id\":" + id.ToString(System.Globalization.CultureInfo.InvariantCulture) + ",\"result\":" + resultJson + "}";

    private static string Failure(long id, Error error) =>
        "{\"id\":" + id.ToString(System.Globalization.CultureInfo.InvariantCulture) + ",\"error\":" + Formatter.Format(error) + "}";

    private sealed class BridgeValidationException : Exception
    {
    }
}

internal sealed record LabelResult(string Label);

internal sealed record DataUrlResult(string DataUrl);

[System.Text.Json.Serialization.JsonSourceGenerationOptions(PropertyNamingPolicy = System.Text.Json.Serialization.JsonKnownNamingPolicy.CamelCase)]
[System.Text.Json.Serialization.JsonSerializable(typeof(LabelResult))]
[System.Text.Json.Serialization.JsonSerializable(typeof(DataUrlResult))]
internal sealed partial class BridgeJson : System.Text.Json.Serialization.JsonSerializerContext
{
}
