using System.Runtime.Versioning;
using System.Security.AccessControl;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Security.Principal;
using Google.Protobuf;
using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Persistence;
using Zafiro.Agent.Local.V1;

namespace Zafiro.Agent.Core.Credentials;

/// <summary>
/// Хранилище Windows (служба под SYSTEM, docs/agent-protocol.md §12.6):
/// <list type="bullet">
/// <item>ключ — именованный постоянный машинный ключ CNG ECDSA P-256 без права экспорта: Microsoft Platform Crypto
/// Provider (TPM), если есть, иначе Microsoft Software Key Storage Provider. Именованный ключ обязателен: SChannel
/// не умеет клиентскую аутентификацию с эфемерным ключом;</item>
/// <item>сертификат и метаданные — файл в %ProgramData%\Zafiro\Agent (ACL: только SYSTEM и администраторы, без
/// наследования; чужая папка или ссылка откладывается — как DeviceVaultStore старого агента), зашифрованный DPAPI
/// учётной записи службы: подменить или прочитать его клиент ПК не может.</item>
/// </list>
/// </summary>
[SupportedOSPlatform("windows")]
public sealed class WindowsCredentialStore : ICredentialStore
{
    private const string KeyPrefix = "Zafiro.Agent.";
    private const string TpmTag = "tpm:";
    private const string SoftwareTag = "sw:";
    private static readonly byte[] Entropy = "Zafiro.Agent.Credentials.v1"u8.ToArray();

    private readonly object _gate = new();
    private readonly string _path;
    private readonly Func<string?> _hardwareUuid;
    private readonly TimeProvider _time;
    private readonly bool _useTpm;

    /// <param name="directory">Каталог состояния (по умолчанию %ProgramData%\Zafiro\Agent).</param>
    /// <param name="useTpm">Пытаться создать ключ в TPM (Platform Crypto Provider).</param>
    public WindowsCredentialStore(string directory, Func<string?>? hardwareUuid = null, TimeProvider? time = null, bool useTpm = true)
    {
        ArgumentException.ThrowIfNullOrEmpty(directory);
        SecureDirectory.Ensure(directory);
        _path = Path.Combine(directory, "credentials.bin");
        _hardwareUuid = hardwareUuid ?? (() => null);
        _time = time ?? TimeProvider.System;
        _useTpm = useTpm;
    }

    public IAgentKey CreateKey()
    {
        var name = KeyPrefix + Guid.NewGuid().ToString("N");
        if (_useTpm)
        {
            try
            {
                return new CngAgentKey(Create(name, CngProvider.MicrosoftPlatformCryptoProvider), TpmTag + name);
            }
            catch (CryptographicException)
            {
                // Нет TPM 2.0 или провайдер недоступен — программный ключ.
            }
        }

        return new CngAgentKey(Create(name, CngProvider.MicrosoftSoftwareKeyStorageProvider), SoftwareTag + name);
    }

    private static CngKey Create(string name, CngProvider provider)
    {
        var p = new CngKeyCreationParameters
        {
            Provider = provider,
            KeyCreationOptions = CngKeyCreationOptions.MachineKey | CngKeyCreationOptions.OverwriteExistingKey,
            ExportPolicy = CngExportPolicies.None,
            KeyUsage = CngKeyUsages.Signing,
        };
        return CngKey.Create(CngAlgorithm.ECDsaP256, name, p);
    }

    private static CngKey? Open(string taggedName)
    {
        var (provider, name) = Split(taggedName);
        if (provider is null || !CngKey.Exists(name, provider, CngKeyOpenOptions.MachineKey)) return null;
        return CngKey.Open(name, provider, CngKeyOpenOptions.MachineKey);
    }

    private static (CngProvider? Provider, string Name) Split(string tagged)
    {
        if (tagged.StartsWith(TpmTag, StringComparison.Ordinal)) return (CngProvider.MicrosoftPlatformCryptoProvider, tagged[TpmTag.Length..]);
        if (tagged.StartsWith(SoftwareTag, StringComparison.Ordinal)) return (CngProvider.MicrosoftSoftwareKeyStorageProvider, tagged[SoftwareTag.Length..]);
        return (null, tagged);
    }

    public AgentCredentials? Load()
    {
        lock (_gate)
        {
            var stored = ReadStored();
            if (stored is null || string.IsNullOrEmpty(stored.KeyName) || string.IsNullOrEmpty(stored.CertificatePem)) return null;
            if (!SameHardware(stored.HardwareUuid)) return null;
            CngKey? key;
            try
            {
                key = Open(stored.KeyName);
            }
            catch (CryptographicException)
            {
                return null;
            }

            if (key is null) return null;
            var ecdsa = new ECDsaCng(key);
            try
            {
                return AgentCredentials.Create(stored, ecdsa, AgentCredentials.IntermediatesFrom(stored.CaChainPem));
            }
            catch (CryptographicException)
            {
                ecdsa.Dispose();
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
            copy.KeyPkcs8 = ByteString.Empty;
            copy.HardwareUuid = _hardwareUuid() ?? "";
            copy.SavedAt = Timestamp.FromDateTimeOffset(_time.GetUtcNow());
            var prev = ReadStored();
            copy.PreviousKeyName = prev is not null && prev.KeyName != key.Name ? prev.KeyName : "";
            WriteStored(copy);
            return AgentCredentials.Create(copy, key.Key, intermediates);
        }
    }

    public void CommitRotation()
    {
        lock (_gate)
        {
            var stored = ReadStored();
            if (stored is null || string.IsNullOrEmpty(stored.PreviousKeyName)) return;
            DeleteByName(stored.PreviousKeyName);
            stored.PreviousKeyName = "";
            WriteStored(stored);
        }
    }

    public void DeleteKey(IAgentKey key)
    {
        ArgumentNullException.ThrowIfNull(key);
        if (key is CngAgentKey cng)
        {
            try
            {
                cng.Cng.Delete();
            }
            catch (CryptographicException)
            {
            }
        }

        key.Dispose();
    }

    public void Clear()
    {
        lock (_gate)
        {
            var stored = ReadStored();
            if (stored is not null)
            {
                DeleteByName(stored.KeyName);
                DeleteByName(stored.PreviousKeyName);
            }

            AtomicFile.Delete(_path);
        }
    }

    private static void DeleteByName(string tagged)
    {
        if (string.IsNullOrEmpty(tagged)) return;
        try
        {
            using var k = Open(tagged);
            k?.Delete();
        }
        catch (CryptographicException)
        {
        }
    }

    private StoredCredentials? ReadStored()
    {
        var bytes = AtomicFile.Read(_path);
        if (bytes is null) return null;
        try
        {
            var plain = ProtectedData.Unprotect(bytes, Entropy, DataProtectionScope.CurrentUser);
            return StoredCredentials.Parser.ParseFrom(plain);
        }
        catch (Exception ex) when (ex is CryptographicException or InvalidProtocolBufferException)
        {
            return null;
        }
    }

    private void WriteStored(StoredCredentials stored) =>
        AtomicFile.Write(_path, ProtectedData.Protect(stored.ToByteArray(), Entropy, DataProtectionScope.CurrentUser));

    private bool SameHardware(string stored)
    {
        var current = _hardwareUuid();
        return string.IsNullOrEmpty(stored) || string.IsNullOrEmpty(current) || string.Equals(stored, current, StringComparison.OrdinalIgnoreCase);
    }

    private sealed class CngAgentKey : IAgentKey
    {
        public CngAgentKey(CngKey key, string name)
        {
            Cng = key;
            Name = name;
            Key = new ECDsaCng(key);
        }

        public CngKey Cng { get; }

        public string Name { get; }

        public ECDsa Key { get; }

        public void Dispose()
        {
            Key.Dispose();
            Cng.Dispose();
        }
    }
}

/// <summary>
/// Папка службы, которой владеют SYSTEM или администраторы, без ссылок (junction); наследование прав выключено.
/// Чужая папка (клиент мог создать её в ProgramData до установки) откладывается и создаётся заново.
/// Перенесено из старого агента: WindowsAgent/ZafiroAgent/Service/DeviceVaultStore.cs CreateTrusted().
/// </summary>
[SupportedOSPlatform("windows")]
public static class SecureDirectory
{
    public static void Ensure(string path)
    {
        var dir = new DirectoryInfo(path);
        for (var attempt = 0; attempt < 3; attempt++)
        {
            dir.Refresh();
            if (dir.Exists && !IsTrusted(dir))
            {
                var aside = dir.FullName + ".untrusted-" + Guid.NewGuid().ToString("N");
                if ((dir.Attributes & FileAttributes.ReparsePoint) != 0) Directory.Delete(dir.FullName); // только ссылка
                else Directory.Move(dir.FullName, aside);
                dir.Refresh();
            }

            if (!dir.Exists) dir.Create(Restricted());
            else dir.SetAccessControl(Restricted());

            dir.Refresh();
            if (IsTrusted(dir)) return;
        }

        throw new UnauthorizedAccessException("The agent data folder could not be secured.");
    }

    private static bool IsTrusted(DirectoryInfo dir)
    {
        if ((dir.Attributes & FileAttributes.ReparsePoint) != 0) return false;
        var owner = dir.GetAccessControl().GetOwner(typeof(SecurityIdentifier)) as SecurityIdentifier;
        return owner is not null && (owner.IsWellKnown(WellKnownSidType.LocalSystemSid) || owner.IsWellKnown(WellKnownSidType.BuiltinAdministratorsSid));
    }

    private static DirectorySecurity Restricted()
    {
        var security = new DirectorySecurity();
        security.SetAccessRuleProtection(isProtected: true, preserveInheritance: false);
        foreach (var sid in new[] { WellKnownSidType.LocalSystemSid, WellKnownSidType.BuiltinAdministratorsSid })
        {
            security.AddAccessRule(new FileSystemAccessRule(new SecurityIdentifier(sid, null), FileSystemRights.FullControl,
                InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit, PropagationFlags.None, AccessControlType.Allow));
        }

        return security;
    }
}
