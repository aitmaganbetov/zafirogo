using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Local.V1;

namespace Zafiro.Agent.Core.Credentials;

/// <summary>
/// Файловое хранилище (Linux — тесты и разработка): ключ PKCS#8 и сертификат в одном файле с правами 0600 в каталоге
/// 0700, доступном только учётной записи службы. На Windows в production не используется (там — CNG, см.
/// <see cref="WindowsCredentialStore"/>): ключ в файле можно скопировать.
/// </summary>
public sealed class FileCredentialStore : ICredentialStore
{
    private readonly object _gate = new();
    private readonly string _path;
    private readonly Func<string?> _hardwareUuid;
    private readonly TimeProvider _time;

    public FileCredentialStore(string directory, Func<string?>? hardwareUuid = null, TimeProvider? time = null)
    {
        ArgumentException.ThrowIfNullOrEmpty(directory);
        AtomicFile.EnsureDirectory(directory);
        _path = Path.Combine(directory, "credentials.pb");
        _hardwareUuid = hardwareUuid ?? (() => null);
        _time = time ?? TimeProvider.System;
    }

    public IAgentKey CreateKey() => new EphemeralKey(ECDsa.Create(ECCurve.NamedCurves.nistP256), "file:" + Guid.NewGuid().ToString("N"));

    public AgentCredentials? Load()
    {
        lock (_gate)
        {
            var stored = ReadStored();
            if (stored is null || stored.KeyPkcs8.IsEmpty || string.IsNullOrEmpty(stored.CertificatePem)) return null;
            if (!SameHardware(stored.HardwareUuid)) return null;
            var key = ECDsa.Create();
            try
            {
                key.ImportPkcs8PrivateKey(stored.KeyPkcs8.Span, out _);
                return AgentCredentials.Create(stored, key, AgentCredentials.IntermediatesFrom(stored.CaChainPem));
            }
            catch (CryptographicException)
            {
                key.Dispose();
                return null;
            }
        }
    }

    public AgentCredentials Save(StoredCredentials stored, IAgentKey key, X509Certificate2Collection intermediates)
    {
        ArgumentNullException.ThrowIfNull(stored);
        ArgumentNullException.ThrowIfNull(key);
        lock (_gate)
        {
            var copy = stored.Clone();
            copy.KeyName = key.Name;
            copy.KeyPkcs8 = ByteString.CopyFrom(key.Key.ExportPkcs8PrivateKey());
            copy.HardwareUuid = _hardwareUuid() ?? "";
            copy.SavedAt = Timestamp.FromDateTimeOffset(_time.GetUtcNow());
            var prev = ReadStored();
            copy.PreviousKeyName = prev is not null && prev.KeyName != key.Name ? prev.KeyName : "";
            AtomicFile.Write(_path, copy.ToByteArray());
            return AgentCredentials.Create(copy, key.Key, intermediates);
        }
    }

    // В файловом хранилище прежний ключ хранится только внутри прежней версии файла — удалять нечего.
    public void CommitRotation()
    {
        lock (_gate)
        {
            var stored = ReadStored();
            if (stored is null || string.IsNullOrEmpty(stored.PreviousKeyName)) return;
            stored.PreviousKeyName = "";
            AtomicFile.Write(_path, stored.ToByteArray());
        }
    }

    public void DeleteKey(IAgentKey key) => key?.Dispose();

    public void Clear()
    {
        lock (_gate) AtomicFile.Delete(_path);
    }

    private StoredCredentials? ReadStored()
    {
        var bytes = AtomicFile.Read(_path);
        if (bytes is null) return null;
        try
        {
            return StoredCredentials.Parser.ParseFrom(bytes);
        }
        catch (InvalidProtocolBufferException)
        {
            return null;
        }
    }

    private bool SameHardware(string stored)
    {
        var current = _hardwareUuid();
        return string.IsNullOrEmpty(stored) || string.IsNullOrEmpty(current) || string.Equals(stored, current, StringComparison.OrdinalIgnoreCase);
    }

    private sealed class EphemeralKey(ECDsa key, string name) : IAgentKey
    {
        public string Name { get; } = name;

        public ECDsa Key { get; } = key;

        public void Dispose() => Key.Dispose();
    }
}
