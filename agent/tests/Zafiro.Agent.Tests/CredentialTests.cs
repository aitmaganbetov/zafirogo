using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Credentials;
using Zafiro.Agent.Core.Enrollment;
using Zafiro.Agent.Local.V1;
using Zafiro.Agent.Tests.Support;

namespace Zafiro.Agent.Tests;

public sealed class CredentialTests : IDisposable
{
    private readonly TestPki _pki = new();
    private readonly TempDir _dir = new();

    public void Dispose()
    {
        _pki.Dispose();
        _dir.Dispose();
    }

    [Fact]
    public void CSR_ECDSA_P256_SHA256()
    {
        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        var pem = Enroller.CreateCsr(key);
        var req = CertificateRequest.LoadSigningRequestPem(pem, HashAlgorithmName.SHA256, CertificateRequestLoadOptions.Default);
        Assert.Equal("1.2.840.10045.2.1", req.PublicKey.Oid.Value); // id-ecPublicKey
        using var pub = req.PublicKey.GetECDsaPublicKey()!;
        Assert.Equal(256, pub.KeySize);
    }

    [Fact]
    public void Выданный_сертификат_проверяется_до_встроенного_корня()
    {
        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        using var leaf = X509Certificate2.CreateFromPem(_pki.IssueClientPem(key));
        using var anchors = _pki.Anchors();
        var intermediates = anchors.ValidateIssued(leaf, [_pki.IssuingPem, _pki.RootPem], key, DateTimeOffset.UtcNow);
        Assert.Single(intermediates); // корень из ответа не берётся — только встроенный
    }

    [Fact]
    public void Чужой_корень_или_чужой_ключ_отклоняются()
    {
        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        using var other = new TestPki("Evil Root");
        using var foreign = X509Certificate2.CreateFromPem(other.IssueClientPem(key));
        using var anchors = _pki.Anchors();
        Assert.Throws<CryptographicException>(() => anchors.ValidateIssued(foreign, [other.IssuingPem, other.RootPem], key, DateTimeOffset.UtcNow));

        using var anotherKey = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        using var leaf = X509Certificate2.CreateFromPem(_pki.IssueClientPem(key));
        Assert.Throws<CryptographicException>(() => anchors.ValidateIssued(leaf, [_pki.IssuingPem], anotherKey, DateTimeOffset.UtcNow));
    }

    [Fact]
    public void Несколько_закреплённых_корней_на_время_ротации_корня()
    {
        // S6-O1-1: RootCaPem/RootCaFile — список корней; сертификат и сервер под любым из них принимаются.
        using var next = new TestPki("Next Zafiro Root");
        // Склейка без перевода строки (cat файлов, где в конце первого нет \n) — тоже два корня.
        using (var glued = Zafiro.Agent.Core.Transport.TrustAnchors.FromPem(_pki.RootPem + next.RootPem))
            Assert.Equal(2, glued.Roots.Count);
        using var anchors = Zafiro.Agent.Core.Transport.TrustAnchors.FromPem(_pki.RootPem + "\n" + next.RootPem);
        Assert.Equal(2, anchors.Roots.Count);
        using var key = ECDsa.Create(ECCurve.NamedCurves.nistP256);
        foreach (var pki in new[] { _pki, next })
        {
            using var leaf = X509Certificate2.CreateFromPem(pki.IssueClientPem(key));
            Assert.Single(anchors.ValidateIssued(leaf, [pki.IssuingPem, pki.RootPem], key, DateTimeOffset.UtcNow));
        }

        var policy = anchors.ServerPolicy();
        Assert.Equal(2, policy.CustomTrustStore.Count);

        using var evil = new TestPki("Evil Root");
        using var foreign = X509Certificate2.CreateFromPem(evil.IssueClientPem(key));
        Assert.Throws<CryptographicException>(() => anchors.ValidateIssued(foreign, [evil.IssuingPem, evil.RootPem], key, DateTimeOffset.UtcNow));
    }

    [Fact]
    public void Корень_должен_быть_самоподписанным()
    {
        Assert.Throws<InvalidOperationException>(() => Zafiro.Agent.Core.Transport.TrustAnchors.FromPem(_pki.IssuingPem));
    }

    [Fact]
    public void Файловое_хранилище_0600_и_привязка_к_железу()
    {
        var store = new FileCredentialStore(_dir.Path, () => "UUID-A");
        Assert.Null(store.Load());
        var key = store.CreateKey();
        var meta = new StoredCredentials
        {
            CertificatePem = _pki.IssueClientPem(key.Key, 1, 9),
            CaChainPem = { _pki.IssuingPem, _pki.RootPem },
            ComputerId = 9, ClubId = 1, AgentEndpoint = "localhost:8443",
            NotAfter = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddDays(30)),
            RenewAfter = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddDays(20)),
        };
        using (var saved = store.Save(meta, key, []))
            Assert.True(saved.Certificate.HasPrivateKey);

        using (var loaded = store.Load())
        {
            Assert.NotNull(loaded);
            Assert.True(loaded!.Certificate.HasPrivateKey);
            Assert.Equal(9, loaded.Stored.ComputerId);
            Assert.Single(loaded.Intermediates);
        }

        if (!OperatingSystem.IsWindows())
        {
            Assert.Equal(UnixFileMode.UserRead | UnixFileMode.UserWrite, File.GetUnixFileMode(Path.Combine(_dir.Path, "credentials.pb")));
            Assert.Equal(UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.UserExecute, File.GetUnixFileMode(_dir.Path));
        }

        // Образ с другого ПК (бездисковая система): данные не подходят этому железу.
        Assert.Null(new FileCredentialStore(_dir.Path, () => "UUID-B").Load());

        store.Clear();
        Assert.Null(store.Load());
    }

    [Fact]
    public void Продление_запоминает_прежний_ключ_до_подтверждения()
    {
        var store = new FileCredentialStore(_dir.Path);
        var k1 = store.CreateKey();
        store.Save(new StoredCredentials { CertificatePem = _pki.IssueClientPem(k1.Key) }, k1, []).Dispose();
        var k2 = store.CreateKey();
        using var renewed = store.Save(new StoredCredentials { CertificatePem = _pki.IssueClientPem(k2.Key) }, k2, []);
        Assert.Equal(k1.Name, renewed.Stored.PreviousKeyName);
        store.CommitRotation();
        using var loaded = store.Load();
        Assert.Equal("", loaded!.Stored.PreviousKeyName);
    }

    [Fact]
    public void Код_регистрации_нормализуется()
    {
        Assert.Equal("ABCDEFGH", EnrollSecret.FromCode(" abcd-efgh ").Code);
        Assert.DoesNotContain("ABCD", EnrollSecret.FromCode("ABCD-EFGH").ToString(), StringComparison.Ordinal);
    }
}
