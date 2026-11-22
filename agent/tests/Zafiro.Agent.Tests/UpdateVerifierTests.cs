using System.Text;
using System.Text.Json;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Microsoft.Extensions.Logging.Abstractions;
using Zafiro.Agent.Core;
using Zafiro.Agent.Core.Updates;
using Zafiro.Agent.Tests.Support;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

/// <summary>
/// Подпись обновления против вектора Go-утилиты server/cmd/sign-update (TestData/gen-update-vector.sh): строка подписи
/// побайтно совпадает с commands.UpdateSigningMessage сервера, иначе подписи CI агент бы не принял.
/// </summary>
public class UpdateVerifierTests
{
    private static string Data(string name) => Path.Combine(AppContext.BaseDirectory, "TestData", name);

    private static (UpdateAvailable Update, Dictionary<string, byte[]> Keys) Vector()
    {
        using var doc = JsonDocument.Parse(File.ReadAllText(Data("update-vector.json")));
        var r = doc.RootElement;
        var u = new UpdateAvailable
        {
            Version = r.GetProperty("version").GetString()!,
            Sha256 = r.GetProperty("sha256").GetString()!,
            SizeBytes = r.GetProperty("size_bytes").GetInt64(),
            Channel = r.GetProperty("channel").GetString()!,
            KeyId = r.GetProperty("key_id").GetString()!,
            SignatureEd25519 = ByteString.FromBase64(r.GetProperty("signature_ed25519").GetString()!),
            Url = r.GetProperty("url").GetString()!,
        };
        var keys = UpdateVerifier.ParseKeys(File.ReadAllText(Data("update-vector.pub")).Trim()).ToDictionary(kv => kv.Key, kv => kv.Value);
        return (u, keys);
    }

    [Fact]
    public void Подпись_утилиты_sign_update_проходит()
    {
        var (u, keys) = Vector();
        Assert.Equal(UpdateRejection.None, new UpdateVerifier(keys).Verify(u));
        Assert.Equal(UpdateRejection.None, new UpdateVerifier(keys, ["github.com"]).Verify(u));
    }

    [Fact]
    public void Строка_подписи_как_UpdateSigningMessage_сервера()
    {
        var (u, _) = Vector();
        var expected = "zafiro-agent-update/v1\n2.0.1\n" + u.Sha256 + "\n51\nstable";
        Assert.Equal(Encoding.UTF8.GetBytes(expected), UpdateVerifier.SigningMessage(u));
    }

    public static TheoryData<string> Tampering => ["version", "sha256", "size", "channel", "signature", "key_id"];

    [Theory]
    [MemberData(nameof(Tampering))]
    public void Изменённое_поле_или_подпись_отклоняются(string field)
    {
        var (u, keys) = Vector();
        switch (field)
        {
            case "version": u.Version = "2.0.2"; break;
            case "sha256": u.Sha256 = new string('0', 64); break;
            case "size": u.SizeBytes++; break;
            case "channel": u.Channel = "beta"; break;
            case "signature":
                var sig = u.SignatureEd25519.ToByteArray();
                sig[10] ^= 0x01;
                u.SignatureEd25519 = ByteString.CopyFrom(sig);
                break;
            case "key_id": u.KeyId = "other"; break;
        }

        var result = new UpdateVerifier(keys).Verify(u);
        Assert.Equal(field == "key_id" ? UpdateRejection.UnknownKey : UpdateRejection.BadSignature, result);
    }

    [Fact]
    public void Чужой_ключ_не_подходит()
    {
        var (u, _) = Vector();
        var other = new Dictionary<string, byte[]> { [u.KeyId] = new byte[32] };
        other[u.KeyId][0] = 1;
        Assert.NotEqual(UpdateRejection.None, new UpdateVerifier(other).Verify(u));
    }

    [Theory]
    [InlineData("http://github.com/x.exe")]
    [InlineData("https://user:pass@github.com/x.exe")]
    [InlineData("ftp://github.com/x.exe")]
    public void Только_https_без_userinfo(string url)
    {
        var (u, keys) = Vector();
        u.Url = url;
        Assert.Equal(UpdateRejection.InvalidFields, new UpdateVerifier(keys).Verify(u));
    }

    [Fact]
    public void Хост_не_из_списка_отклоняется()
    {
        var (u, keys) = Vector();
        u.Url = "https://evil.example/ZafiroAgentSetup.exe";
        Assert.Equal(UpdateRejection.HostNotAllowed, new UpdateVerifier(keys, ["github.com"]).Verify(u));
    }

    [Fact]
    public void Поля_как_validateUpdate_сервера()
    {
        var (u, _) = Vector();
        Assert.True(UpdateVerifier.ValidFields(u));
        Assert.False(UpdateVerifier.ValidFields(new UpdateAvailable(u) { Sha256 = u.Sha256.ToUpperInvariant() }));
        Assert.False(UpdateVerifier.ValidFields(new UpdateAvailable(u) { Version = "2.0.1\nx" }));
        Assert.False(UpdateVerifier.ValidFields(new UpdateAvailable(u) { SizeBytes = 0 }));
        Assert.False(UpdateVerifier.ValidFields(new UpdateAvailable(u) { SignatureEd25519 = ByteString.CopyFrom(new byte[63]) }));
        Assert.False(UpdateVerifier.ValidFields(new UpdateAvailable(u) { KeyId = "" }));
    }

    [Fact]
    public async Task Файл_проверяется_по_размеру_и_sha256()
    {
        var (u, _) = Vector();
        await using (var f = File.OpenRead(Data("update-vector.bin")))
            Assert.True(await UpdateVerifier.VerifyFileAsync(f, u, CancellationToken.None));

        var bytes = await File.ReadAllBytesAsync(Data("update-vector.bin"));
        bytes[0] ^= 0x20;
        await using var changed = new MemoryStream(bytes);
        Assert.False(await UpdateVerifier.VerifyFileAsync(changed, u, CancellationToken.None));
        await using var longer = new MemoryStream([.. bytes, 0]);
        Assert.False(await UpdateVerifier.VerifyFileAsync(longer, u, CancellationToken.None));
    }

    [Theory]
    [InlineData("k1")]
    [InlineData("k1:AAAA")]
    public void Неверный_формат_ключей_ошибка(string spec) => Assert.Throws<FormatException>(() => UpdateVerifier.ParseKeys(spec));
    // SEC-5 (аудит 22.11.2026): защита от отката — подписанный, но не более новый выпуск не ставится.
    [Theory]
    [InlineData("2.0.1", "2.0.0", true)]
    [InlineData("2.0.10", "2.0.9", true)]
    [InlineData("2.1.0", "2.0.99", true)]
    [InlineData("10.0.0", "9.9.9", true)]
    [InlineData("2.0.1", "2.0.1", false)]
    [InlineData("2.0.1-beta", "2.0.1", false)]
    [InlineData("2.0.0", "2.0.1", false)]
    [InlineData("1.9.9", "2.0.0", false)]
    [InlineData("1.0.0", "2.3.4", false)]
    [InlineData("v3.0.0", "2.0.0", false)]
    [InlineData("3.0", "2.0.0", false)]
    [InlineData("", "2.0.0", false)]
    [InlineData("99999999999999999999.0.0", "2.0.0", false)]
    [InlineData("2.0.1", "dev", true)]
    [InlineData("2.0.1", "", true)]
    public void Версия_новее_установленной(string candidate, string current, bool newer) =>
        Assert.Equal(newer, UpdateVerifier.IsNewer(candidate, current));

    [Fact]
    public void Подписанный_выпуск_не_новее_своей_версии_отклоняется()
    {
        var (u, keys) = Vector(); // 2.0.1, подпись верная
        var v = new UpdateVerifier(keys);
        Assert.Equal(UpdateRejection.None, v.Verify(u, "2.0.0"));
        Assert.Equal(UpdateRejection.NotNewer, v.Verify(u, "2.0.1"));
        Assert.Equal(UpdateRejection.NotNewer, v.Verify(u, "3.0.0"));
        // Неверная подпись — по-прежнему BadSignature, а не NotNewer.
        Assert.Equal(UpdateRejection.BadSignature, v.Verify(new UpdateAvailable(u) { Channel = "beta" }, "3.0.0"));
    }

    [Fact]
    public async Task Агент_не_откатывается_на_подписанную_старую_версию()
    {
        var (u, keys) = Vector();
        using var dir = new TempDir();
        using var pki = new TestPki();
        var platform = new FakePlatform();
        var handler = new CountingHandler();
        var installer = new UpdateInstaller(platform, new HttpClient(handler), ["github.com"], NullLogger.Instance);
        await using var rt = Runtimes.Create(dir.Path, platform, pki, keys, installer,
            options: new AgentOptions { EnrollEndpoint = "localhost:1", AgentVersion = "3.1.0" });
        var cmd = new Command
        {
            CommandId = Guid.CreateVersion7().ToString(), Seq = 1, IssuedAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow),
            UpdateAvailable = new UpdateAvailable(u) { InstallNow = true },
        };
        var r = await rt.HandleCommandAsync(cmd, CancellationToken.None);
        Assert.Equal(CommandStatus.Failed, r.Status);
        Assert.Equal(ErrorCode.UpdateVerificationFailed, r.ErrorCode);
        await Task.Delay(100);
        Assert.Equal(0, handler.Requests);
        Assert.Equal(0, platform.Installs);
    }

    private sealed class CountingHandler : HttpMessageHandler
    {
        public int Requests;

        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            Interlocked.Increment(ref Requests);
            return Task.FromResult(new HttpResponseMessage(System.Net.HttpStatusCode.NotFound));
        }
    }
}
