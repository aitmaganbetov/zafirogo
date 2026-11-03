using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Runtime.Versioning;
using Zafiro.Agent.Core.Health;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Service.Platform;

// Источники здоровья Windows без драйверов ядра и без System.Management (несовместим с Native AOT): COM вызывается
// напрямую по таблице методов (unmanaged function pointers), Win32 — source-generated P/Invoke. Номера слотов —
// порядок методов в заголовках Windows SDK (WbemCli.h, mmdeviceapi.h, propsys.h), первые три — IUnknown.

/// <summary>COM: инициализация потока, создание объектов, вызовы по таблице методов.</summary>
[SupportedOSPlatform("windows")]
internal static unsafe partial class ComNative
{
    private const int RpcEChangedMode = unchecked((int)0x80010106);

    [LibraryImport("ole32.dll")]
    private static partial int CoInitializeEx(nint reserved, uint coInit);

    [LibraryImport("ole32.dll")]
    private static partial void CoUninitialize();

    [LibraryImport("ole32.dll")]
    public static partial int CoCreateInstance(Guid* clsid, nint outer, uint context, Guid* iid, nint* instance);

    [LibraryImport("ole32.dll")]
    public static partial int CoSetProxyBlanket(nint proxy, uint authnService, uint authzService, nint principal, uint authnLevel,
        uint impersonationLevel, nint authInfo, uint capabilities);

    [LibraryImport("ole32.dll")]
    public static partial int PropVariantClear(NativeVariant* value);

    [LibraryImport("oleaut32.dll")]
    public static partial int VariantClear(NativeVariant* value);

    [LibraryImport("oleaut32.dll", StringMarshalling = StringMarshalling.Utf16)]
    public static partial nint SysAllocString(string value);

    [LibraryImport("oleaut32.dll")]
    public static partial void SysFreeString(nint value);

    /// <summary>Таблица методов COM-объекта.</summary>
    public static void** Vtbl(nint obj) => *(void***)obj;

    public static void Release(nint obj)
    {
        if (obj != 0) ((delegate* unmanaged[Stdcall]<nint, uint>)Vtbl(obj)[2])(obj);
    }

    public static void Check(int hr, string what)
    {
        if (hr < 0) throw new ExternalException(what + " failed: 0x" + hr.ToString("X8", System.Globalization.CultureInfo.InvariantCulture), hr);
    }

    /// <summary>COM (MTA) на время вызова; поток уже в STA — работаем в нём.</summary>
    public static Scope Enter()
    {
        var hr = CoInitializeEx(0, 0); // COINIT_MULTITHREADED
        if (hr < 0 && hr != RpcEChangedMode) Check(hr, "CoInitializeEx");
        return new Scope(hr >= 0);
    }

    public sealed class Scope(bool owns) : IDisposable
    {
        private bool _owns = owns;

        public void Dispose()
        {
            if (!_owns) return;
            _owns = false;
            CoUninitialize();
        }
    }
}

/// <summary>VARIANT / PROPVARIANT (на x64 — 24 байта, с запасом 32): тип и значение со смещения 8.</summary>
[StructLayout(LayoutKind.Explicit, Size = 32)]
internal struct NativeVariant
{
    [FieldOffset(0)]
    public ushort Type;

    [FieldOffset(8)]
    public sbyte I1;

    [FieldOffset(8)]
    public byte U1;

    [FieldOffset(8)]
    public short I2;

    [FieldOffset(8)]
    public ushort U2;

    [FieldOffset(8)]
    public int I4;

    [FieldOffset(8)]
    public uint U4;

    [FieldOffset(8)]
    public long I8;

    [FieldOffset(8)]
    public nint Pointer;

    /// <summary>Целое (VT_I1…VT_UI8, VT_INT/VT_UINT) или строка (VT_BSTR, VT_LPWSTR); прочее — null.</summary>
    public readonly object? ToObject() => Type switch
    {
        16 => (long)I1,
        17 => (long)U1,
        2 => (long)I2,
        18 => (long)U2,
        3 or 22 => (long)I4,
        19 or 23 => (long)U4,
        20 or 21 => I8,
        8 => Pointer == 0 ? null : Marshal.PtrToStringBSTR(Pointer),
        31 => Pointer == 0 ? null : Marshal.PtrToStringUni(Pointer),
        _ => null,
    };
}

/// <summary>WMI без System.Management: IWbemLocator → IWbemServices.ExecQuery → IEnumWbemClassObject.Next → IWbemClassObject.Get.</summary>
[SupportedOSPlatform("windows")]
internal static unsafe class Wmi
{
    private static readonly Guid ClsidWbemLocator = new("4590f811-1d3a-11d0-891f-00aa004b2e24");
    private static readonly Guid IidWbemLocator = new("dc12a687-737f-11cf-884d-00aa004b2e24");
    private const int MaxRows = 256;

    /// <summary>Строки запроса WQL: значения <paramref name="properties"/> (long, string или null). Должен вызываться внутри <see cref="ComNative.Enter"/>.</summary>
    public static List<object?[]> Query(string ns, string wql, string[] properties, TimeSpan timeout)
    {
        var clsid = ClsidWbemLocator;
        var iid = IidWbemLocator;
        nint locator = 0, services = 0, enumerator = 0, bNs = 0, bLang = 0, bQuery = 0;
        try
        {
            ComNative.Check(ComNative.CoCreateInstance(&clsid, 0, 1 /* CLSCTX_INPROC_SERVER */, &iid, &locator), "WbemLocator");
            bNs = ComNative.SysAllocString(ns);
            // IWbemLocator::ConnectServer — слот 3; WBEM_FLAG_CONNECT_USE_MAX_WAIT (0x80): не дольше 2 минут.
            var connect = (delegate* unmanaged[Stdcall]<nint, nint, nint, nint, nint, int, nint, nint, nint*, int>)ComNative.Vtbl(locator)[3];
            ComNative.Check(connect(locator, bNs, 0, 0, 0, 0x80, 0, 0, &services), "ConnectServer " + ns);
            Blanket(services);
            bLang = ComNative.SysAllocString("WQL");
            bQuery = ComNative.SysAllocString(wql);
            // IWbemServices::ExecQuery — 18-й метод после IUnknown (слот 20); WBEM_FLAG_FORWARD_ONLY | WBEM_FLAG_RETURN_IMMEDIATELY.
            var exec = (delegate* unmanaged[Stdcall]<nint, nint, nint, int, nint, nint*, int>)ComNative.Vtbl(services)[20];
            ComNative.Check(exec(services, bLang, bQuery, 0x20 | 0x10, 0, &enumerator), "ExecQuery");
            Blanket(enumerator);
            // IEnumWbemClassObject::Next — слот 4.
            var next = (delegate* unmanaged[Stdcall]<nint, int, uint, nint*, uint*, int>)ComNative.Vtbl(enumerator)[4];
            var rows = new List<object?[]>();
            var deadline = Environment.TickCount64 + (long)timeout.TotalMilliseconds;
            while (rows.Count < MaxRows)
            {
                var left = deadline - Environment.TickCount64;
                if (left <= 0) break;
                nint obj = 0;
                uint got = 0;
                ComNative.Check(next(enumerator, (int)Math.Min(left, int.MaxValue), 1, &obj, &got), "IEnumWbemClassObject.Next");
                if (got == 0) break; // WBEM_S_FALSE (конец) или WBEM_S_TIMEDOUT
                try
                {
                    rows.Add(Read(obj, properties));
                }
                finally
                {
                    ComNative.Release(obj);
                }
            }

            return rows;
        }
        finally
        {
            ComNative.Release(enumerator);
            ComNative.Release(services);
            ComNative.Release(locator);
            ComNative.SysFreeString(bQuery);
            ComNative.SysFreeString(bLang);
            ComNative.SysFreeString(bNs);
        }
    }

    /// <summary>Прокси WMI: олицетворение (RPC_C_IMP_LEVEL_IMPERSONATE), иначе провайдеры отвечают «доступ запрещён».</summary>
    private static void Blanket(nint proxy) =>
        _ = ComNative.CoSetProxyBlanket(proxy, 10 /* RPC_C_AUTHN_WINNT */, 0 /* RPC_C_AUTHZ_NONE */, 0, 3 /* RPC_C_AUTHN_LEVEL_CALL */,
            3 /* RPC_C_IMP_LEVEL_IMPERSONATE */, 0, 0 /* EOAC_NONE */);

    private static object?[] Read(nint obj, string[] properties)
    {
        // IWbemClassObject::Get — слот 4 (после GetQualifierSet).
        var get = (delegate* unmanaged[Stdcall]<nint, char*, int, NativeVariant*, int*, int*, int>)ComNative.Vtbl(obj)[4];
        var values = new object?[properties.Length];
        for (var i = 0; i < properties.Length; i++)
        {
            NativeVariant v = default;
            fixed (char* name = properties[i])
            {
                if (get(obj, name, 0, &v, null, null) >= 0) values[i] = v.ToObject();
            }

            _ = ComNative.VariantClear(&v);
        }

        return values;
    }
}

/// <summary>Аудиовыходы (MMDevice API): активные устройства вывода, форм-фактор и название.</summary>
[SupportedOSPlatform("windows")]
internal static unsafe class AudioEndpoints
{
    private static readonly Guid ClsidEnumerator = new("bcde0395-e52f-467c-8e3d-c4579291692e");
    private static readonly Guid IidEnumerator = new("a95664d2-9614-4f35-a746-de8db63617e6");
    private static readonly PropertyKey FriendlyName = new(new Guid("a45c254e-df1c-4efd-8020-67d146a850e0"), 14);
    private static readonly PropertyKey FormFactor = new(new Guid("1da5d803-d492-4edd-8c23-e0c0ffee7f0e"), 0);

    [StructLayout(LayoutKind.Sequential)]
    private readonly struct PropertyKey(Guid fmtid, uint pid)
    {
        public readonly Guid FormatId = fmtid;
        public readonly uint PropertyId = pid;
    }

    /// <summary>Гарнитуры (<see cref="HealthParsing.IsHeadsetFormFactor"/>): сначала наушники/гарнитуры по форм-фактору.</summary>
    public static List<PeripheralDevice> Headsets()
    {
        using var com = ComNative.Enter();
        var clsid = ClsidEnumerator;
        var iid = IidEnumerator;
        nint enumerator = 0, collection = 0;
        var found = new List<(bool Headphones, string Name)>();
        try
        {
            ComNative.Check(ComNative.CoCreateInstance(&clsid, 0, 0x17 /* CLSCTX_ALL */, &iid, &enumerator), "MMDeviceEnumerator");
            // IMMDeviceEnumerator::EnumAudioEndpoints(eRender, DEVICE_STATE_ACTIVE) — слот 3.
            var enumEndpoints = (delegate* unmanaged[Stdcall]<nint, int, uint, nint*, int>)ComNative.Vtbl(enumerator)[3];
            ComNative.Check(enumEndpoints(enumerator, 0, 1, &collection), "EnumAudioEndpoints");
            // IMMDeviceCollection::GetCount — слот 3, Item — слот 4.
            uint count;
            ComNative.Check(((delegate* unmanaged[Stdcall]<nint, uint*, int>)ComNative.Vtbl(collection)[3])(collection, &count), "GetCount");
            var item = (delegate* unmanaged[Stdcall]<nint, uint, nint*, int>)ComNative.Vtbl(collection)[4];
            for (uint i = 0; i < count && i < 32; i++)
            {
                nint device = 0, store = 0;
                try
                {
                    if (item(collection, i, &device) < 0) continue;
                    // IMMDevice::OpenPropertyStore(STGM_READ) — слот 4.
                    if (((delegate* unmanaged[Stdcall]<nint, uint, nint*, int>)ComNative.Vtbl(device)[4])(device, 0, &store) < 0) continue;
                    var ff = Property(store, FormFactor) is long f ? (uint)f : 10u; // нет — UnknownFormFactor
                    if (!HealthParsing.IsHeadsetFormFactor(ff)) continue;
                    found.Add((HealthParsing.IsHeadphonesFormFactor(ff), Property(store, FriendlyName) as string ?? ""));
                }
                finally
                {
                    ComNative.Release(store);
                    ComNative.Release(device);
                }
            }
        }
        finally
        {
            ComNative.Release(collection);
            ComNative.Release(enumerator);
        }

        return found.OrderByDescending(x => x.Headphones).Select(x => new PeripheralDevice(PeripheralKind.Headset, x.Name)).ToList();
    }

    private static object? Property(nint store, PropertyKey key)
    {
        // IPropertyStore::GetValue — слот 5.
        var getValue = (delegate* unmanaged[Stdcall]<nint, PropertyKey*, NativeVariant*, int>)ComNative.Vtbl(store)[5];
        NativeVariant v = default;
        try
        {
            return getValue(store, &key, &v) >= 0 ? v.ToObject() : null;
        }
        finally
        {
            _ = ComNative.PropVariantClear(&v);
        }
    }
}

/// <summary>Устройства PnP (SetupAPI): мыши и клавиатуры (HID — USB/Bluetooth), мониторы (название из EDID).</summary>
[SupportedOSPlatform("windows")]
internal static unsafe partial class PnpDevices
{
    public static readonly Guid MouseClass = new("4d36e96f-e325-11ce-bfc1-08002be10318");
    public static readonly Guid KeyboardClass = new("4d36e96b-e325-11ce-bfc1-08002be10318");
    public static readonly Guid MonitorClass = new("4d36e96e-e325-11ce-bfc1-08002be10318");

    private const uint DigcfPresent = 0x2;
    private const uint SpdrpDeviceDesc = 0x0;
    private const uint SpdrpFriendlyName = 0xC;
    private const uint DevpropTypeString = 0x12;
    private static readonly DevPropKey BusReportedDeviceDesc = new() { FormatId = new Guid("540b947e-8b40-45bc-a8a2-6a0b894cbda2"), PropertyId = 4 };

    [StructLayout(LayoutKind.Sequential)]
    private struct SpDevInfoData
    {
        public uint CbSize;
        public Guid ClassGuid;
        public uint DevInst;
        public nuint Reserved;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct DevPropKey
    {
        public Guid FormatId;
        public uint PropertyId;
    }

    [LibraryImport("setupapi.dll", SetLastError = true)]
    private static partial nint SetupDiGetClassDevsW(Guid* classGuid, char* enumerator, nint parent, uint flags);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetupDiEnumDeviceInfo(nint set, uint index, SpDevInfoData* data);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetupDiDestroyDeviceInfoList(nint set);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetupDiGetDeviceInstanceIdW(nint set, SpDevInfoData* data, char* buffer, uint size, uint* required);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetupDiGetDevicePropertyW(nint set, SpDevInfoData* data, DevPropKey* key, uint* type, byte* buffer, uint size,
        uint* required, uint flags);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetupDiGetDeviceRegistryPropertyW(nint set, SpDevInfoData* data, uint property, uint* regType, byte* buffer,
        uint size, uint* required);

    [LibraryImport("setupapi.dll", SetLastError = true)]
    private static partial nint SetupDiOpenDevRegKey(nint set, SpDevInfoData* data, uint scope, uint hwProfile, uint keyType, uint access);

    [LibraryImport("advapi32.dll", StringMarshalling = StringMarshalling.Utf16)]
    private static partial int RegQueryValueExW(nint key, string name, nint reserved, uint* type, byte* data, uint* size);

    [LibraryImport("advapi32.dll")]
    private static partial int RegCloseKey(nint key);

    /// <summary>
    /// Подключённые устройства класса, чей ID экземпляра начинается с <paramref name="instancePrefix"/>. Название — описание
    /// от шины (строка продукта USB/Bluetooth), иначе понятное имя/описание драйвера; монитор — имя из EDID (кэш по ID).
    /// </summary>
    public static List<PeripheralDevice> Present(PeripheralKind kind, Guid classGuid, string instancePrefix, Dictionary<string, string> edidNames)
    {
        var set = SetupDiGetClassDevsW(&classGuid, null, 0, DigcfPresent);
        if (set == -1) throw new Win32Exception(Marshal.GetLastPInvokeError());
        try
        {
            var result = new List<PeripheralDevice>();
            var data = new SpDevInfoData { CbSize = (uint)sizeof(SpDevInfoData) };
            for (uint i = 0; i < 256 && SetupDiEnumDeviceInfo(set, i, &data); i++)
            {
                var id = InstanceId(set, &data);
                if (id is null || !id.StartsWith(instancePrefix, StringComparison.OrdinalIgnoreCase)) continue;
                string? name = null;
                if (kind == PeripheralKind.Monitor && !edidNames.TryGetValue(id, out name))
                {
                    name = EdidName(set, &data);
                    if (name is not null && edidNames.Count < 64) edidNames[id] = name;
                }

                name ??= StringProperty(set, &data) ?? RegistryString(set, &data, SpdrpFriendlyName) ?? RegistryString(set, &data, SpdrpDeviceDesc) ?? "";
                result.Add(new PeripheralDevice(kind, name));
            }

            return result;
        }
        finally
        {
            SetupDiDestroyDeviceInfoList(set);
        }
    }

    private static string? InstanceId(nint set, SpDevInfoData* data)
    {
        var buf = stackalloc char[512];
        uint required;
        return SetupDiGetDeviceInstanceIdW(set, data, buf, 512, &required) ? new string(buf) : null;
    }

    private static string? StringProperty(nint set, SpDevInfoData* data)
    {
        var buf = stackalloc byte[512];
        uint type, required;
        var key = BusReportedDeviceDesc;
        if (!SetupDiGetDevicePropertyW(set, data, &key, &type, buf, 512, &required, 0) || type != DevpropTypeString) return null;
        return Text(buf, required);
    }

    private static string? RegistryString(nint set, SpDevInfoData* data, uint property)
    {
        var buf = stackalloc byte[512];
        uint type, required;
        if (!SetupDiGetDeviceRegistryPropertyW(set, data, property, &type, buf, 512, &required) || type != 1 /* REG_SZ */) return null;
        return Text(buf, required);
    }

    private static string? Text(byte* buf, uint bytes)
    {
        var s = new string((char*)buf, 0, (int)Math.Min(bytes, 512) / 2).TrimEnd('\0').Trim();
        return s.Length == 0 ? null : s;
    }

    private static string? EdidName(nint set, SpDevInfoData* data)
    {
        // DICS_FLAG_GLOBAL, DIREG_DEV, KEY_READ: раздел «Device Parameters» монитора, значение EDID.
        var key = SetupDiOpenDevRegKey(set, data, 1, 0, 1, 0x20019);
        if (key == -1 || key == 0) return null;
        try
        {
            var buf = stackalloc byte[1024];
            uint type, size = 1024;
            if (RegQueryValueExW(key, "EDID", 0, &type, buf, &size) != 0 || type != 3 /* REG_BINARY */) return null;
            return HealthParsing.EdidName(new ReadOnlySpan<byte>(buf, (int)Math.Min(size, 1024u)));
        }
        finally
        {
            _ = RegCloseKey(key);
        }
    }
}

/// <summary>Журнал System (wevtapi, Windows Event Log API): число событий по запросу XPath.</summary>
[SupportedOSPlatform("windows")]
internal static unsafe partial class SystemEventLog
{
    private const uint EvtQueryChannelPath = 1;
    private const int ErrorNoMoreItems = 259;
    private const uint MaxCount = 10000;

    [LibraryImport("wevtapi.dll", SetLastError = true, StringMarshalling = StringMarshalling.Utf16)]
    private static partial nint EvtQuery(nint session, string path, string query, uint flags);

    [LibraryImport("wevtapi.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool EvtNext(nint resultSet, uint eventsSize, nint* events, uint timeout, uint flags, uint* returned);

    [LibraryImport("wevtapi.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool EvtClose(nint handle);

    public static uint Count(string xpath)
    {
        var query = EvtQuery(0, "System", xpath, EvtQueryChannelPath);
        if (query == 0) throw new Win32Exception(Marshal.GetLastPInvokeError());
        try
        {
            var events = stackalloc nint[64];
            uint total = 0;
            while (total < MaxCount)
            {
                uint got = 0;
                if (!EvtNext(query, 64, events, 5000, 0, &got))
                {
                    var err = Marshal.GetLastPInvokeError();
                    if (err == ErrorNoMoreItems) break;
                    throw new Win32Exception(err);
                }

                for (var i = 0; i < got; i++) EvtClose(events[i]);
                total += got;
            }

            return total;
        }
        finally
        {
            EvtClose(query);
        }
    }
}

/// <summary>Том → номера физических дисков (IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS, доступ к тому без чтения данных).</summary>
[SupportedOSPlatform("windows")]
internal static unsafe partial class VolumeDisks
{
    private const uint IoctlVolumeGetVolumeDiskExtents = 0x00560000;

    [LibraryImport("kernel32.dll", SetLastError = true, StringMarshalling = StringMarshalling.Utf16)]
    private static partial nint CreateFileW(string name, uint access, uint share, nint security, uint disposition, uint flags, nint template);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DeviceIoControl(nint device, uint code, void* input, uint inputSize, void* output, uint outputSize, uint* returned,
        nint overlapped);

    /// <summary>«C:» → номера дисков; пусто — не удалось.</summary>
    public static IReadOnlyList<uint> Of(string mount)
    {
        if (mount.Length != 2 || mount[1] != ':') return [];
        var h = CreateFileW(@"\\.\" + mount, 0, 3 /* FILE_SHARE_READ | FILE_SHARE_WRITE */, 0, 3 /* OPEN_EXISTING */, 0, 0);
        if (h == -1) return [];
        try
        {
            var buf = stackalloc byte[8 + 24 * 16];
            uint returned;
            if (!DeviceIoControl(h, IoctlVolumeGetVolumeDiskExtents, null, 0, buf, 8 + 24 * 16, &returned, 0)) return [];
            return HealthParsing.VolumeDiskNumbers(new ReadOnlySpan<byte>(buf, (int)returned));
        }
        finally
        {
            WindowsNative.CloseHandle(h);
        }
    }
}

/// <summary>
/// NVIDIA NVML (nvml.dll из драйвера видеокарты; путь — только System32 или папка NVSMI в Program Files, без поиска
/// по PATH): температура, загрузка, занятая память первой видеокарты. Нет драйвера — повтор не чаще раза в 10 минут.
/// </summary>
[SupportedOSPlatform("windows")]
internal sealed unsafe class Nvml
{
    private static readonly long RetryMs = (long)TimeSpan.FromMinutes(10).TotalMilliseconds;
    private nint _lib;
    private bool _ready;
    private long _retryAt;
    private delegate* unmanaged[Cdecl]<int> _init;
    private delegate* unmanaged[Cdecl]<int> _shutdown;
    private delegate* unmanaged[Cdecl]<uint*, int> _count;
    private delegate* unmanaged[Cdecl]<uint, nint*, int> _handle;
    private delegate* unmanaged[Cdecl]<nint, int, uint*, int> _temperature;
    private delegate* unmanaged[Cdecl]<nint, Utilization*, int> _utilization;
    private delegate* unmanaged[Cdecl]<nint, Memory*, int> _memory;

    [StructLayout(LayoutKind.Sequential)]
    private struct Utilization
    {
        public uint Gpu;
        public uint Memory;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct Memory
    {
        public ulong Total;
        public ulong Free;
        public ulong Used;
    }

    /// <summary>Показания; null — нет NVIDIA или NVML не ответила.</summary>
    public GpuReading? Read()
    {
        if (!EnsureReady()) return null;
        uint count;
        nint device;
        if (_count(&count) != 0 || count == 0 || _handle(0, &device) != 0)
        {
            Reset(); // драйвер обновили/видеокарта пропала — переинициализация позже
            return null;
        }

        uint temp;
        var t = _temperature(device, 0 /* NVML_TEMPERATURE_GPU */, &temp) == 0 ? (int)Math.Min(temp, 300u) * 10 : 0;
        Utilization u;
        Memory m;
        var loadOk = _utilization(device, &u) == 0;
        var memOk = _memory(device, &m) == 0 && m.Total > 0;
        return new GpuReading(t,
            loadOk ? Math.Min(100u, u.Gpu) * 100 : 0,
            memOk ? (uint)Math.Min(10000ul, m.Used * 10000 / m.Total) : 0,
            loadOk && memOk);
    }

    private bool EnsureReady()
    {
        if (_ready) return true;
        if (Environment.TickCount64 < _retryAt) return false;
        _retryAt = Environment.TickCount64 + RetryMs;
        var candidates = new[]
        {
            Path.Combine(Environment.SystemDirectory, "nvml.dll"),
            Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "NVIDIA Corporation", "NVSMI", "nvml.dll"),
        };
        foreach (var path in candidates)
        {
            if (File.Exists(path) && NativeLibrary.TryLoad(path, out _lib)) break;
        }

        if (_lib == 0) return false;
        if (!Export("nvmlInit_v2", out var init) || !Export("nvmlShutdown", out var shutdown) || !Export("nvmlDeviceGetCount_v2", out var count)
            || !Export("nvmlDeviceGetHandleByIndex_v2", out var handle) || !Export("nvmlDeviceGetTemperature", out var temperature)
            || !Export("nvmlDeviceGetUtilizationRates", out var utilization) || !Export("nvmlDeviceGetMemoryInfo", out var memory))
        {
            Unload();
            return false;
        }

        _init = (delegate* unmanaged[Cdecl]<int>)init;
        _shutdown = (delegate* unmanaged[Cdecl]<int>)shutdown;
        _count = (delegate* unmanaged[Cdecl]<uint*, int>)count;
        _handle = (delegate* unmanaged[Cdecl]<uint, nint*, int>)handle;
        _temperature = (delegate* unmanaged[Cdecl]<nint, int, uint*, int>)temperature;
        _utilization = (delegate* unmanaged[Cdecl]<nint, Utilization*, int>)utilization;
        _memory = (delegate* unmanaged[Cdecl]<nint, Memory*, int>)memory;
        if (_init() != 0)
        {
            Unload();
            return false;
        }

        _ready = true;
        return true;
    }

    private bool Export(string name, out nint address) => NativeLibrary.TryGetExport(_lib, name, out address);

    private void Reset()
    {
        if (_ready) _ = _shutdown();
        _ready = false;
        Unload();
    }

    private void Unload()
    {
        if (_lib != 0) NativeLibrary.Free(_lib);
        _lib = 0;
    }
}
