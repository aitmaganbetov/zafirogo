using System.Runtime.Versioning;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Persistence;

namespace Zafiro.Agent.Service;

/// <summary>Что сделала <see cref="ImageKeyStore.Resolve"/> при старте службы (для журнала).</summary>
public enum ImageKeyMigration
{
    /// <summary>Ключа образа нет (обычный ПК).</summary>
    None,

    /// <summary>Ключ взят из защищённого файла.</summary>
    Loaded,

    /// <summary>Ключ перенесён из appsettings.json в защищённый файл, в appsettings.json он стёрт.</summary>
    Migrated,

    /// <summary>Ключ сохранён в защищённый файл, но стереть его из конфигурации не удалось (переменная окружения, файл только для чтения).</summary>
    MigratedButStillInConfig,
}

/// <summary>
/// Ключ образа бездисковых ПК (решение Q-037, open-questions/stage5-agent.md AG-10) — не в открытом appsettings.json:
/// Windows — DPAPI LocalMachine (один ключ на все ПК образа: DPAPI_SYSTEM общий у загрузившихся из одного образа) в
/// папке данных службы (только SYSTEM и администраторы, <see cref="SecureDirectory"/>); Linux — файл 0600 в каталоге 0700.
/// При старте значение из конфигурации (Agent:ImageKey) переносится в файл и стирается из appsettings.json.
/// Бездисковый образ: перенос нужно сделать в режиме правки образа (супер-рабочая станция), иначе запись пропадёт при
/// перезагрузке и ключ снова окажется в appsettings.json образа — установщик вызывает <c>--set-image-key</c> сам.
/// </summary>
public static class ImageKeyStore
{
    public const string FileName = "image-key.bin";
    private const int MaxKeyLength = 256;
    private static readonly byte[] Entropy = "Zafiro.Agent.ImageKey.v1"u8.ToArray();

    /// <summary>Проверка формата: непустой, без пробелов и управляющих символов, ≤ 256 символов.</summary>
    public static bool IsValid(string? key) =>
        !string.IsNullOrEmpty(key) && key.Length <= MaxKeyLength && key.All(c => c > ' ' && c < 0x7F);

    /// <summary>
    /// Ключ для этого запуска: из конфигурации (тогда он переносится в файл и стирается из appsettings.json) или из файла.
    /// </summary>
    public static (string? Key, ImageKeyMigration Result) Resolve(string? configured, string dataDirectory, string? appSettingsPath)
    {
        ArgumentException.ThrowIfNullOrEmpty(dataDirectory);
        if (!string.IsNullOrEmpty(configured))
        {
            if (!IsValid(configured)) throw new InvalidDataException("Agent:ImageKey has an invalid format.");
            Save(dataDirectory, configured);
            var erased = appSettingsPath is not null && EraseFromAppSettings(appSettingsPath);
            return (configured, erased ? ImageKeyMigration.Migrated : ImageKeyMigration.MigratedButStillInConfig);
        }

        var stored = Load(dataDirectory);
        return stored is null ? (null, ImageKeyMigration.None) : (stored, ImageKeyMigration.Loaded);
    }

    /// <summary>Сохраняет ключ (установщик: <c>Zafiro.Agent.Service --set-image-key</c>, ключ — из stdin).</summary>
    public static void Save(string dataDirectory, string key)
    {
        if (!IsValid(key)) throw new ArgumentException("Invalid image key.", nameof(key));
        var plain = Encoding.UTF8.GetBytes(key);
        try
        {
            if (OperatingSystem.IsWindows())
            {
                SecureDirectory.Ensure(dataDirectory);
                AtomicFile.Write(Path.Combine(dataDirectory, FileName), Protect(plain));
            }
            else
            {
                AtomicFile.EnsureDirectory(dataDirectory);
                AtomicFile.Write(Path.Combine(dataDirectory, FileName), plain);
            }
        }
        finally
        {
            CryptographicOperations.ZeroMemory(plain);
        }
    }

    /// <summary>Ключ из файла; null — файла нет или он испорчен (чужая машина, ручная правка).</summary>
    public static string? Load(string dataDirectory)
    {
        var path = Path.Combine(dataDirectory, FileName);
        var bytes = AtomicFile.Read(path);
        if (bytes is null) return null;
        try
        {
            var plain = OperatingSystem.IsWindows() ? Unprotect(bytes) : bytes;
            var key = Encoding.UTF8.GetString(plain);
            return IsValid(key) ? key : null;
        }
        catch (CryptographicException)
        {
            return null;
        }
    }

    /// <summary>Удаляет ключ (снятие ПК с бездисковой схемы).</summary>
    public static void Delete(string dataDirectory) => AtomicFile.Delete(Path.Combine(dataDirectory, FileName));

    /// <summary>
    /// Стирает Agent:ImageKey из appsettings.json (остальное без изменений, атомарно). false — файла нет, ключа в нём нет
    /// или записать нельзя (тогда ключ остаётся в конфигурации, служба пишет предупреждение).
    /// </summary>
    public static bool EraseFromAppSettings(string path)
    {
        ArgumentException.ThrowIfNullOrEmpty(path);
        try
        {
            var bytes = AtomicFile.Read(path);
            if (bytes is null) return false;
            var root = JsonNode.Parse(bytes, documentOptions: new JsonDocumentOptions { CommentHandling = JsonCommentHandling.Skip, AllowTrailingCommas = true });
            if (root?["Agent"] is not JsonObject agent || agent["ImageKey"] is not JsonValue value ||
                !value.TryGetValue<string>(out var current) || string.IsNullOrEmpty(current))
            {
                return false;
            }

            agent["ImageKey"] = "";
            using var buffer = new MemoryStream();
            using (var writer = new Utf8JsonWriter(buffer, new JsonWriterOptions { Indented = true }))
            {
                root.WriteTo(writer);
            }

            AtomicFile.Write(path, buffer.ToArray());
            return true;
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException or JsonException)
        {
            return false;
        }
    }

    [SupportedOSPlatform("windows")]
    private static byte[] Protect(byte[] plain) => ProtectedData.Protect(plain, Entropy, DataProtectionScope.LocalMachine);

    [SupportedOSPlatform("windows")]
    private static byte[] Unprotect(byte[] data) => ProtectedData.Unprotect(data, Entropy, DataProtectionScope.LocalMachine);
}
