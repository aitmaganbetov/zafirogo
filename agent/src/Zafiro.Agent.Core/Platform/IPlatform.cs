using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Platform;

/// <summary>
/// Действия на ПК, которые выполняет служба по командам сервера. Windows — реальные вызовы (перенесены из старого
/// агента: shutdown.exe, Wake-on-LAN, запуск UI в сеансе пользователя), Linux — заглушка для тестов.
/// </summary>
public interface IPlatform
{
    /// <summary>Сведения о ПК (DeviceInfo) для Enroll/Hello.</summary>
    DeviceInfo GetDeviceInfo(string agentVersion, bool diskless);

    /// <summary>UUID текущей загрузки ОС (новый после каждой перезагрузки) — Hello.boot_id.</summary>
    string GetBootId();

    /// <summary>Загрузка ПК (Status.load). idle_seconds заполняет служба по данным UI.</summary>
    ComputerLoad GetLoad();

    /// <summary>Что умеет агент на этой платформе (Hello.capabilities).</summary>
    AgentCapabilities Capabilities { get; }

    /// <summary>Перезагрузка через <paramref name="delay"/>. false — ОС отказала.</summary>
    Task<bool> RebootAsync(TimeSpan delay, CancellationToken cancellationToken);

    /// <summary>Выключение через <paramref name="delay"/>.</summary>
    Task<bool> ShutdownAsync(TimeSpan delay, CancellationToken cancellationToken);

    /// <summary>Wake-on-LAN: «магический пакет» на MAC-адреса.</summary>
    Task<int> WakeOnLanAsync(IReadOnlyCollection<string> macAddresses, CancellationToken cancellationToken);

    /// <summary>Запуск проверенного установщика обновления (тихая установка). false — не запустился.</summary>
    Task<bool> InstallUpdateAsync(string installerPath, CancellationToken cancellationToken);

    /// <summary>Каталог для скачивания обновлений (доступен только службе).</summary>
    string UpdateDirectory { get; }
}
