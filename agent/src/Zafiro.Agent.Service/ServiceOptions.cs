namespace Zafiro.Agent.Service;

/// <summary>Настройки хоста службы (секция "Service" в appsettings.json / ZAFIRO_AGENT_Service__*).</summary>
public sealed class ServiceOptions
{
    /// <summary>Имя службы Windows.</summary>
    public const string ServiceName = "ZafiroAgent";

    /// <summary>Именованный канал IPC (Windows).</summary>
    public string PipeName { get; set; } = "ZafiroAgent.Ipc";

    /// <summary>Unix socket IPC (Linux).</summary>
    public string SocketPath { get; set; } = "/run/zafiro-agent/ipc.sock";

    /// <summary>
    /// uid, которым разрешено подключаться к unix socket (кроме uid самой службы). Пусто — только uid службы.
    /// </summary>
    public List<int> AllowedUids { get; set; } = [];

    /// <summary>
    /// Путь к exe UI (WPF/WebView2). Пусто — на Windows Zafiro.Agent.UI.exe рядом со службой (установщик кладёт их
    /// вместе); на Linux UI не запускается (только IPC для тестов).
    /// </summary>
    public string UiExecutable { get; set; } = "";

    /// <summary>Хосты, с которых можно скачивать установщик обновления.</summary>
    public List<string> UpdateHosts { get; set; } = ["github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"];

    /// <summary>
    /// Дополнительные открытые ключи подписи обновлений "key_id:base64" — только в окружении Development (тесты).
    /// В production ключи только встроенные (<see cref="UpdateKeys"/>).
    /// </summary>
    public string DevUpdatePublicKeys { get; set; } = "";

    /// <summary>Запускать UI и под администраторами (галочка установщика, как LaunchForAdministrators старого агента).</summary>
    public bool LaunchUiForAdministrators { get; set; }
}

/// <summary>
/// Открытые ключи Ed25519 подписи обновлений, встроенные в агент (решение «Обновления агента», docs/DECISIONS.md;
/// docs/agent-protocol.md §6 п. 2): key_id → base64 32 байт. Пара к секрету CI AGENT_UPDATE_SIGNING_KEY; новый ключ
/// добавляется выпуском, подписанным старым. Пока список пуст — UpdateAvailable отклоняется (UNKNOWN_KEY).
/// </summary>
public static class UpdateKeys
{
    public static readonly IReadOnlyDictionary<string, string> Embedded = new Dictionary<string, string>(StringComparer.Ordinal)
    {
        // "2026-10" = "<base64 открытого ключа>" — добавить при создании ключа выпуска (docs/open-questions/stage5-agent.md).
    };
}
