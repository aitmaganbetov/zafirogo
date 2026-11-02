using System.Buffers.Binary;
using System.Net.Sockets;
using System.Runtime.Versioning;
using Microsoft.Extensions.Logging;

namespace Zafiro.Agent.Service.Ipc;

/// <summary>
/// IPC на Linux (тесты, разработка): unix socket; клиент проверяется по uid из SO_PEERCRED — разрешены uid службы и
/// <see cref="ServiceOptions.AllowedUids"/>. Файл сокета — 0600 (0666, если разрешены другие uid: доступ всё равно
/// решает проверка uid).
/// </summary>
[UnsupportedOSPlatform("windows")]
public sealed class UnixSocketListener : IIpcListener
{
    private const int SolSocket = 1;
    private const int SoPeerCred = 17;

    private readonly Socket _socket;
    private readonly string _path;
    private readonly HashSet<int> _allowed;
    private readonly ILogger _log;

    public UnixSocketListener(string path, IEnumerable<int> allowedUids, ILogger<UnixSocketListener> log)
    {
        ArgumentException.ThrowIfNullOrEmpty(path);
        _path = path;
        _log = log;
        _allowed = [.. allowedUids ?? [], CurrentUid()];
        var dir = Path.GetDirectoryName(Path.GetFullPath(path))!;
        Directory.CreateDirectory(dir);
        if (File.Exists(path)) File.Delete(path); // сокет прежнего запуска
        _socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        _socket.Bind(new UnixDomainSocketEndPoint(path));
        File.SetUnixFileMode(path, _allowed.Count > 1
            ? UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.GroupRead | UnixFileMode.GroupWrite | UnixFileMode.OtherRead | UnixFileMode.OtherWrite
            : UnixFileMode.UserRead | UnixFileMode.UserWrite);
        _socket.Listen(16);
    }

    public async Task<IpcClient> AcceptAsync(CancellationToken cancellationToken)
    {
        while (true)
        {
            var s = await _socket.AcceptAsync(cancellationToken).ConfigureAwait(false);
            var (pid, uid) = PeerCredentials(s);
            if (uid >= 0 && _allowed.Contains(uid)) return new IpcClient(new NetworkStream(s, ownsSocket: true), $"uid={uid} pid={pid}");
            _log.LogWarning("ipc client rejected: uid={Uid} pid={Pid}", uid, pid);
            s.Dispose();
        }
    }

    /// <summary>struct ucred { pid_t pid; uid_t uid; gid_t gid; } из SO_PEERCRED.</summary>
    public static (int Pid, int Uid) PeerCredentials(Socket s)
    {
        ArgumentNullException.ThrowIfNull(s);
        Span<byte> buf = stackalloc byte[12];
        try
        {
            var n = s.GetRawSocketOption(SolSocket, SoPeerCred, buf);
            if (n < 12) return (-1, -1);
            return (BinaryPrimitives.ReadInt32LittleEndian(buf), BinaryPrimitives.ReadInt32LittleEndian(buf[4..]));
        }
        catch (SocketException)
        {
            return (-1, -1);
        }
    }

    /// <summary>uid процесса (владелец /proc/self).</summary>
    public static int CurrentUid()
    {
        try
        {
            foreach (var line in File.ReadLines("/proc/self/status"))
            {
                if (!line.StartsWith("Uid:", StringComparison.Ordinal)) continue;
                var parts = line.Split('\t', ' ').Where(p => p.Length > 0).ToArray();
                return int.Parse(parts[1], System.Globalization.CultureInfo.InvariantCulture);
            }
        }
        catch (Exception ex) when (ex is IOException or FormatException or UnauthorizedAccessException)
        {
        }

        return -1;
    }

    public ValueTask DisposeAsync()
    {
        _socket.Dispose();
        try
        {
            File.Delete(_path);
        }
        catch (IOException)
        {
        }

        return ValueTask.CompletedTask;
    }
}
