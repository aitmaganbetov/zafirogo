using System.IO.Pipes;
using System.Runtime.Versioning;
using System.Security.AccessControl;
using System.Security.Principal;
using Microsoft.Extensions.Logging;
using Zafiro.Agent.Service.Platform;

namespace Zafiro.Agent.Service.Ipc;

/// <summary>
/// IPC на Windows: именованный канал. Создавать экземпляры канала могут только SYSTEM и администраторы (программа
/// клиента не займёт имя), подключаться — вошедшие пользователи. Клиент принимается, только если:
/// <list type="bullet">
/// <item>он в активном сеансе консоли (GetNamedPipeClientSessionId);</item>
/// <item>его exe — UI из папки установки (если <see cref="ServiceOptions.UiExecutable"/> задан; как IsAgentImage
/// сторожа старого агента);</item>
/// <item>его SID (UI подключается с TokenImpersonationLevel.Identification) — пользователь этого сеанса, SYSTEM или
/// администратор.</item>
/// </list>
/// Перенесено и усилено: WindowsAgent/ZafiroAgent/Service/VaultServer.cs CreatePipe(), VaultProtocol.cs.
/// </summary>
[SupportedOSPlatform("windows")]
public sealed class NamedPipeListener : IIpcListener
{
    private readonly string _name;
    private readonly string? _uiExe;
    private readonly ILogger _log;

    public NamedPipeListener(string name, string? uiExe, ILogger<NamedPipeListener> log)
    {
        _name = name;
        _uiExe = string.IsNullOrWhiteSpace(uiExe) ? null : Path.GetFullPath(uiExe);
        _log = log;
    }

    public async Task<IpcClient> AcceptAsync(CancellationToken cancellationToken)
    {
        while (true)
        {
            var pipe = Create();
            try
            {
                await pipe.WaitForConnectionAsync(cancellationToken).ConfigureAwait(false);
            }
            catch
            {
                await pipe.DisposeAsync().ConfigureAwait(false);
                throw;
            }

            var identity = Verify(pipe);
            if (identity is not null) return new IpcClient(pipe, identity);
            await pipe.DisposeAsync().ConfigureAwait(false);
        }
    }

    private NamedPipeServerStream Create()
    {
        var security = new PipeSecurity();
        security.AddAccessRule(new PipeAccessRule(new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null), PipeAccessRights.FullControl, AccessControlType.Allow));
        security.AddAccessRule(new PipeAccessRule(new SecurityIdentifier(WellKnownSidType.BuiltinAdministratorsSid, null), PipeAccessRights.FullControl, AccessControlType.Allow));
        security.AddAccessRule(new PipeAccessRule(new SecurityIdentifier(WellKnownSidType.AuthenticatedUserSid, null), PipeAccessRights.ReadWrite, AccessControlType.Allow));
        return NamedPipeServerStreamAcl.Create(_name, PipeDirection.InOut, NamedPipeServerStream.MaxAllowedServerInstances,
            PipeTransmissionMode.Byte, PipeOptions.Asynchronous, 0, 0, security);
    }

    private string? Verify(NamedPipeServerStream pipe)
    {
        var handle = pipe.SafePipeHandle.DangerousGetHandle();
        if (!WindowsNative.GetNamedPipeClientProcessId(handle, out var pid) || !WindowsNative.GetNamedPipeClientSessionId(handle, out var session))
            return Reject("client process is unknown", 0, 0);
        var console = WindowsNative.WTSGetActiveConsoleSessionId();
        if (session != console) return Reject("not the console session", pid, session);

        if (_uiExe is not null)
        {
            var image = WindowsNative.ProcessImagePath(pid);
            if (image is null || !string.Equals(Path.GetFullPath(image), _uiExe, StringComparison.OrdinalIgnoreCase))
                return Reject("not the agent UI executable", pid, session);
        }

        SecurityIdentifier? client = null;
        try
        {
            pipe.RunAsClient(() => client = WindowsIdentity.GetCurrent(TokenAccessLevels.Query).User);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or System.Security.SecurityException)
        {
            return Reject("client does not allow identification", pid, session);
        }

        if (client is null) return Reject("client SID is unknown", pid, session);
        var sessionUser = WindowsNative.SessionUser(session);
        var trusted = (sessionUser is not null && client.Equals(sessionUser)) || client.IsWellKnown(WellKnownSidType.LocalSystemSid);
        if (!trusted) return Reject("client SID is not the session user", pid, session);
        return $"pid={pid} session={session} sid={client.Value}";
    }

    private string? Reject(string reason, uint pid, uint session)
    {
        _log.LogWarning("ipc client rejected ({Reason}): pid={Pid} session={Session}", reason, pid, session);
        return null;
    }

    public ValueTask DisposeAsync() => ValueTask.CompletedTask;
}
