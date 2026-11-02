using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Platform;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Core.Transport;
using Zafiro.Agent.Core.Updates;
using Zafiro.Agent.Service;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Service.Platform;
using Zafiro.Agent.Service.Ui;

if (args.Contains("--version"))
{
    Console.WriteLine(typeof(AgentRuntime).Assembly.GetName().Version?.ToString(3) ?? "0.0.0");
    return 0;
}

var builder = Host.CreateApplicationBuilder(new HostApplicationBuilderSettings
{
    Args = args,
    ContentRootPath = AppContext.BaseDirectory,
});
builder.Configuration.AddEnvironmentVariables("ZAFIRO_AGENT_");

var agent = new AgentOptions();
builder.Configuration.GetSection("Agent").Bind(agent);
var service = new ServiceOptions();
builder.Configuration.GetSection("Service").Bind(service);
agent.AgentVersion = typeof(AgentRuntime).Assembly.GetName().Version?.ToString(3) ?? agent.AgentVersion;
if (string.IsNullOrEmpty(agent.DataDirectory))
{
    agent.DataDirectory = OperatingSystem.IsWindows()
        ? Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.CommonApplicationData), "Zafiro", "Agent")
        : Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "zafiro-agent");
}

// Корень CA (AG-3): установщик кладёт root-ca.pem рядом со службой; относительный путь — от папки программы
// (рабочая папка службы Windows — System32).
if (string.IsNullOrWhiteSpace(agent.RootCaPem))
{
    if (string.IsNullOrWhiteSpace(agent.RootCaFile) && File.Exists(Path.Combine(AppContext.BaseDirectory, "root-ca.pem")))
        agent.RootCaFile = "root-ca.pem";
    if (!string.IsNullOrWhiteSpace(agent.RootCaFile) && !Path.IsPathRooted(agent.RootCaFile))
        agent.RootCaFile = Path.Combine(AppContext.BaseDirectory, agent.RootCaFile);
}

// UI (WPF + WebView2) ставится рядом со службой: путь по умолчанию — для запуска UI и проверки exe клиента канала IPC.
if (OperatingSystem.IsWindows() && string.IsNullOrWhiteSpace(service.UiExecutable))
{
    service.UiExecutable = Path.Combine(AppContext.BaseDirectory, "Zafiro.Agent.UI.exe");
}

// AG-10: ключ образа — не в открытом appsettings.json (DPAPI LocalMachine / файл 0600, ImageKeyStore).
if (args.Contains("--set-image-key"))
{
    // Установщик: ключ — первой строкой stdin (не в командной строке: её видят другие процессы).
    var key = Console.In.ReadLine()?.Trim();
    if (!ImageKeyStore.IsValid(key))
    {
        Console.Error.WriteLine("invalid image key");
        return 2;
    }

    ImageKeyStore.Save(agent.DataDirectory, key!);
    ImageKeyStore.EraseFromAppSettings(Path.Combine(AppContext.BaseDirectory, "appsettings.json"));
    return 0;
}

var (imageKey, imageKeyMigration) = ImageKeyStore.Resolve(agent.ImageKey, agent.DataDirectory, Path.Combine(AppContext.BaseDirectory, "appsettings.json"));
agent.ImageKey = imageKey;

if (OperatingSystem.IsWindows())
{
    builder.Services.AddWindowsService(o => o.ServiceName = ServiceOptions.ServiceName);
}

builder.Services.AddSingleton(agent);
builder.Services.AddSingleton(service);
builder.Services.AddSingleton(TimeProvider.System);
builder.Services.AddSingleton(_ => TrustAnchors.FromOptions(agent));
builder.Services.AddSingleton<IPlatform>(sp => OperatingSystem.IsWindows()
    ? new WindowsPlatform(sp.GetRequiredService<ILogger<WindowsPlatform>>(), agent.DataDirectory)
    : new LinuxPlatform(sp.GetRequiredService<ILogger<LinuxPlatform>>(), agent.DataDirectory));
builder.Services.AddSingleton<ICredentialStore>(sp =>
{
    var platform = sp.GetRequiredService<IPlatform>();
    string? Uuid() => platform.GetDeviceInfo(agent.AgentVersion, agent.Diskless).HardwareUuid;
    return OperatingSystem.IsWindows()
        ? new WindowsCredentialStore(agent.DataDirectory, Uuid)
        : new FileCredentialStore(agent.DataDirectory, Uuid);
});
builder.Services.AddSingleton(sp =>
{
    var keys = UpdateKeys.Embedded.ToDictionary(kv => kv.Key, kv => Convert.FromBase64String(kv.Value), StringComparer.Ordinal);
    if (sp.GetRequiredService<IHostEnvironment>().IsDevelopment())
    {
        foreach (var (id, key) in UpdateVerifier.ParseKeys(service.DevUpdatePublicKeys)) keys[id] = key;
    }

    return new UpdateVerifier(keys, service.UpdateHosts);
});
builder.Services.AddSingleton(sp => new UpdateInstaller(sp.GetRequiredService<IPlatform>(), UpdateInstaller.CreateHttpClient(), service.UpdateHosts,
    sp.GetRequiredService<ILoggerFactory>().CreateLogger("Zafiro.Agent.Update")));
builder.Services.AddSingleton(sp => new AgentRuntime(agent, sp.GetRequiredService<ICredentialStore>(), sp.GetRequiredService<IPlatform>(),
    sp.GetRequiredService<TrustAnchors>(), sp.GetRequiredService<UpdateVerifier>(), sp.GetRequiredService<UpdateInstaller>(),
    sp.GetRequiredService<ILoggerFactory>(), sp.GetRequiredService<TimeProvider>()));
builder.Services.AddSingleton<IpcDispatcher>();
builder.Services.AddSingleton<IIpcListener>(sp => OperatingSystem.IsWindows()
    ? new NamedPipeListener(service.PipeName, service.UiExecutable, sp.GetRequiredService<ILogger<NamedPipeListener>>())
    : CreateUnixListener(sp));
builder.Services.AddSingleton<IUiLauncher>(sp => OperatingSystem.IsWindows()
    ? new WindowsUiLauncher(service, sp.GetRequiredService<ILogger<WindowsUiLauncher>>())
    : new NoUiLauncher());
builder.Services.AddSingleton<IpcServer>();
builder.Services.AddHostedService<AgentWorker>();
builder.Services.AddHostedService(sp => sp.GetRequiredService<IpcServer>());
builder.Services.AddHostedService<UiCoordinator>();

var host = builder.Build();
var startupLog = host.Services.GetRequiredService<ILoggerFactory>().CreateLogger("Zafiro.Agent.Service");
switch (imageKeyMigration)
{
    case ImageKeyMigration.Migrated:
        startupLog.LogInformation("image key moved from appsettings.json to the protected store");
        break;
    case ImageKeyMigration.MigratedButStillInConfig:
        startupLog.LogWarning("image key saved to the protected store but is still present in the configuration (environment or read-only appsettings.json)");
        break;
}

await host.RunAsync().ConfigureAwait(false);
return 0;

IIpcListener CreateUnixListener(IServiceProvider sp)
{
    if (OperatingSystem.IsWindows()) throw new PlatformNotSupportedException();
    return new UnixSocketListener(service.SocketPath, service.AllowedUids, sp.GetRequiredService<ILogger<UnixSocketListener>>());
}

/// <summary>Держит связь с сервером, пока работает служба.</summary>
internal sealed class AgentWorker(AgentRuntime runtime) : BackgroundService
{
    protected override Task ExecuteAsync(CancellationToken stoppingToken) => runtime.RunAsync(stoppingToken);
}
