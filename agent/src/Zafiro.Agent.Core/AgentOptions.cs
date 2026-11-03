namespace Zafiro.Agent.Core;

/// <summary>
/// Настройки службы агента (Zafiro.Agent.Service читает их из appsettings / переменных ZAFIRO_AGENT__* / реестра
/// HKLM\SOFTWARE\Zafiro\Agent, куда их пишет установщик).
/// </summary>
public sealed class AgentOptions
{
    /// <summary>Адрес регистрации ("host:port") — EnrollmentService.Enroll без клиентского сертификата.</summary>
    public string EnrollEndpoint { get; set; } = "agent.zafiro.kz:443";

    /// <summary>
    /// Корень CA Zafiro (PEM): сервер проверяется только по нему (pinning), системное хранилище не используется.
    /// Пусто — <see cref="RootCaFile"/>.
    /// </summary>
    public string? RootCaPem { get; set; }

    /// <summary>Файл корня CA (PEM; несколько сертификатов — на время ротации корня).</summary>
    public string? RootCaFile { get; set; }

    /// <summary>Каталог состояния службы (журналы команд и событий, учётные данные на Linux).</summary>
    public string DataDirectory { get; set; } = "";

    /// <summary>Версия агента (SemVer) — Hello.agent_version и заголовок zafiro-agent-version.</summary>
    public string AgentVersion { get; set; } = "2.0.0";

    /// <summary>Язык ПК (ru | kk | en) — заголовок zafiro-locale и Hello.locale.</summary>
    public string Locale { get; set; } = "ru";

    /// <summary>Ключ образа клуба для бездисковых ПК ("zik_…", решение Q-037): регистрация после каждой загрузки без кода.</summary>
    public string? ImageKey { get; set; }

    /// <summary>ПК бездисковый (DeviceInfo.diskless).</summary>
    public bool Diskless { get; set; }

    /// <summary>Таймаут unary-вызовов, меняющих данные (вход, покупки) — agent.proto п. 9.</summary>
    public TimeSpan WriteDeadline { get; set; } = TimeSpan.FromSeconds(15);

    /// <summary>Таймаут unary-вызовов чтения.</summary>
    public TimeSpan ReadDeadline { get; set; } = TimeSpan.FromSeconds(5);

    /// <summary>Сколько ждать Welcome после Hello.</summary>
    public TimeSpan WelcomeTimeout { get; set; } = TimeSpan.FromSeconds(30);

    /// <summary>Как часто проверять продление сертификата, истечение сессии и техрежима.</summary>
    public TimeSpan HousekeepingInterval { get; set; } = TimeSpan.FromSeconds(1);

    /// <summary>Как часто опрашивать периферию (подключение/отключение — Status сразу). Полный снимок здоровья — раз в status_interval.</summary>
    public TimeSpan HealthPollInterval { get; set; } = TimeSpan.FromSeconds(5);

    /// <summary>Сколько ждать источники здоровья за один сбор; дольше — сбор считается зависшим, health не шлётся.</summary>
    public TimeSpan HealthSourceTimeout { get; set; } = TimeSpan.FromSeconds(20);
}
