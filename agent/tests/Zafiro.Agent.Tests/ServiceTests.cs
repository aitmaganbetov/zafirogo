using System.Net.Sockets;
using Google.Protobuf;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.Service.Ipc;
using Zafiro.Agent.Service.Platform;
using Zafiro.Agent.Service.Ui;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>Служба: IPC (кадры, unix socket с проверкой uid), разбор SMBIOS, Wake-on-LAN, пауза запуска UI.</summary>
public sealed class ServiceTests : IDisposable
{
    private readonly TempDir _dir = new();
    private readonly TestPki _pki = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    [Fact]
    public async Task Кадр_IPC_туда_и_обратно()
    {
        var env = new Envelope { Request = new Request { Id = 7, Method = "agent.GetSnapshot", Payload = new GetSnapshotRequest().ToByteString() } };
        using var ms = new MemoryStream();
        await IpcFraming.WriteAsync(ms, env, CancellationToken.None);
        ms.Position = 0;
        Assert.Equal(env, await IpcFraming.ReadAsync(ms, CancellationToken.None));
        Assert.Null(await IpcFraming.ReadAsync(ms, CancellationToken.None));
    }

    [Fact]
    public async Task Слишком_большой_кадр_отклоняется()
    {
        using var ms = new MemoryStream([0xFF, 0xFF, 0xFF, 0x7F]);
        await Assert.ThrowsAsync<InvalidDataException>(() => IpcFraming.ReadAsync(ms, CancellationToken.None));
    }

    [Fact]
    public async Task UI_по_unix_socket_получает_снимок_и_вызывает_методы()
    {
        if (OperatingSystem.IsWindows()) return;
        await using var runtime = Runtimes.Create(_dir.Path, new FakePlatform(), _pki);
        var socket = Path.Combine(_dir.Path, "ipc.sock");
        var listener = new UnixSocketListener(socket, [], NullLogger<UnixSocketListener>.Instance);
        var server = new IpcServer(listener, new IpcDispatcher(runtime), runtime, NullLogger<IpcServer>.Instance);
        using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(20));
        await server.StartAsync(cts.Token);
        try
        {
            Assert.Equal(UnixFileMode.UserRead | UnixFileMode.UserWrite, File.GetUnixFileMode(socket));
            using var client = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
            await client.ConnectAsync(new UnixDomainSocketEndPoint(socket), cts.Token);
            await using var stream = new NetworkStream(client, ownsSocket: false);

            var first = await IpcFraming.ReadAsync(stream, cts.Token);
            Assert.Equal(Connection.NotEnrolled, first!.Event.Snapshot.Connection);
            Assert.Equal(Screen.Setup, first.Event.Snapshot.Screen);

            await IpcFraming.WriteAsync(stream, new Envelope { Request = new Request { Id = 1, Method = "agent.GetSnapshot", Payload = new GetSnapshotRequest().ToByteString() } }, cts.Token);
            await IpcFraming.WriteAsync(stream, new Envelope { Request = new Request { Id = 2, Method = "Nope/Nope" } }, cts.Token);
            // Гость не вошёл: токена нет — GUEST_TOKEN_INVALID без похода на сервер.
            await IpcFraming.WriteAsync(stream, new Envelope { Request = new Request { Id = 3, Method = "GuestService/ListTariffs", Payload = new ListTariffsRequest().ToByteString() } }, cts.Token);

            var responses = new Dictionary<ulong, Response>();
            while (responses.Count < 3)
            {
                var env = await IpcFraming.ReadAsync(stream, cts.Token);
                if (env!.KindCase == Envelope.KindOneofCase.Response) responses[env.Response.Id] = env.Response;
            }

            Assert.Null(responses[1].Error);
            Assert.Equal(Connection.NotEnrolled, Snapshot.Parser.ParseFrom(responses[1].Payload).Connection);
            Assert.Equal((int)Grpc.Core.StatusCode.Unimplemented, responses[2].Error.GrpcStatus);
            Assert.Equal(ErrorCode.GuestTokenInvalid, responses[3].Error.ErrorCode);

            // Изменение состояния приходит событием.
            runtime.State.SetConnection(Connection.Connecting);
            while (true)
            {
                var env = await IpcFraming.ReadAsync(stream, cts.Token);
                if (env!.KindCase == Envelope.KindOneofCase.Event && env.Event.Snapshot?.Connection == Connection.Connecting) break;
            }
        }
        finally
        {
            await server.StopAsync(CancellationToken.None);
        }
    }

    [Fact]
    public void Peercred_своего_процесса()
    {
        if (OperatingSystem.IsWindows()) return;
        var path = Path.Combine(_dir.Path, "p.sock");
        using var listen = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        listen.Bind(new UnixDomainSocketEndPoint(path));
        listen.Listen(1);
        using var c = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
        c.Connect(new UnixDomainSocketEndPoint(path));
        using var s = listen.Accept();
        var (pid, uid) = UnixSocketListener.PeerCredentials(s);
        Assert.Equal(Environment.ProcessId, pid);
        Assert.Equal(UnixSocketListener.CurrentUid(), uid);
    }

    [Fact]
    public void Методы_IPC_покрывают_все_RPC_гостя()
    {
        var expected = new[]
        {
            GuestAuthService.Descriptor, GuestService.Descriptor, ShopService.Descriptor, SocialService.Descriptor,
        }.SelectMany(s => s.Methods.Select(m => s.Name + "/" + m.Name));
        using var dir = new TempDir();
        var runtime = Runtimes.Create(dir.Path, new FakePlatform(), _pki);
        var methods = new IpcDispatcher(runtime).Methods;
        Assert.All(expected, m => Assert.Contains(m, methods));
    }

    [Fact]
    public void Магический_пакет_WoL()
    {
        var p = WakeOnLanSender.MagicPacket("AA:BB:CC:DD:EE:FF");
        Assert.Equal(102, p.Length);
        Assert.All(p[..6], b => Assert.Equal(0xFF, b));
        Assert.Equal(new byte[] { 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF }, p[96..]);
        Assert.Throws<FormatException>(() => WakeOnLanSender.MagicPacket("AA:BB"));
    }

    [Fact]
    public void SMBIOS_UUID_из_структуры_типа_1()
    {
        var uuid = new byte[] { 0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF };
        var type0 = new byte[] { 0, 4, 0, 0, (byte)'A', 0, 0 };
        var type1 = new byte[0x1B];
        type1[0] = 1;
        type1[1] = 0x1B;
        uuid.CopyTo(type1, 8);
        var table = type0.Concat(type1).Concat(new byte[] { 0, 0 }).ToArray();
        var raw = new byte[8 + table.Length];
        BitConverter.GetBytes(table.Length).CopyTo(raw, 4);
        table.CopyTo(raw, 8);
        Assert.Equal("00112233-4455-6677-8899-AABBCCDDEEFF", Smbios.ParseUuid(raw));

        Array.Clear(type1, 8, 16);
        table = type0.Concat(type1).Concat(new byte[] { 0, 0 }).ToArray();
        table.CopyTo(raw, 8);
        Assert.Null(Smbios.ParseUuid(raw)); // заглушка производителя
    }

    [Theory]
    [InlineData(1, 3)]
    [InlineData(2, 6)]
    [InlineData(3, 12)]
    [InlineData(6, 60)]
    [InlineData(10, 60)]
    public void Пауза_перезапуска_UI(int failures, int seconds) => Assert.Equal(TimeSpan.FromSeconds(seconds), UiCoordinator.RetryDelay(failures));
}
