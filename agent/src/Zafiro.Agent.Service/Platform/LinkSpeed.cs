using System.Net;
using System.Net.NetworkInformation;
using System.Net.Sockets;

namespace Zafiro.Agent.Service.Platform;

/// <summary>
/// Скорость интерфейса, через который идёт связь с сервером: адрес сервера (DNS — с таймаутом, кэш 10 мин) →
/// UDP-сокет Connect (пакеты не отправляются, ОС только выбирает маршрут) → локальный адрес → интерфейс с этим адресом →
/// NetworkInterface.Speed. Не вышло — основной интерфейс (<see cref="NetworkSelector"/>: работает, есть шлюз, Ethernet).
/// </summary>
public sealed class LinkSpeed
{
    private static readonly TimeSpan DnsTimeout = TimeSpan.FromSeconds(3);
    private static readonly TimeSpan DnsCache = TimeSpan.FromMinutes(10);
    private string? _host;
    private IPAddress? _address;
    private long _resolvedAt;

    public uint ForServer(string? host)
    {
        var nics = NetworkInterface.GetAllNetworkInterfaces();
        NetworkInterface? nic = null;
        var local = LocalAddressTowards(host);
        if (local is not null)
        {
            nic = nics.FirstOrDefault(n => n.OperationalStatus == OperationalStatus.Up
                                           && n.GetIPProperties().UnicastAddresses.Any(a => a.Address.Equals(local)));
        }

        nic ??= nics.Where(n => n.OperationalStatus == OperationalStatus.Up
                                && n.NetworkInterfaceType is not (NetworkInterfaceType.Loopback or NetworkInterfaceType.Tunnel))
            .OrderByDescending(n => n.GetIPProperties().GatewayAddresses.Count > 0)
            .ThenByDescending(n => n.NetworkInterfaceType == NetworkInterfaceType.Ethernet)
            .FirstOrDefault();
        return nic is null ? 0 : HealthParsing.ToMbps(nic.Speed);
    }

    private IPAddress? LocalAddressTowards(string? host)
    {
        var remote = Resolve(host);
        if (remote is null || IPAddress.IsLoopback(remote)) return null;
        try
        {
            using var s = new Socket(remote.AddressFamily, SocketType.Dgram, ProtocolType.Udp);
            s.Connect(new IPEndPoint(remote, 443));
            return (s.LocalEndPoint as IPEndPoint)?.Address;
        }
        catch (SocketException)
        {
            return null;
        }
    }

    private IPAddress? Resolve(string? host)
    {
        if (string.IsNullOrEmpty(host)) return null;
        if (IPAddress.TryParse(host, out var ip)) return ip;
        if (host == _host && _address is not null && Environment.TickCount64 - _resolvedAt < (long)DnsCache.TotalMilliseconds) return _address;
        try
        {
            var all = Dns.GetHostAddressesAsync(host).WaitAsync(DnsTimeout).GetAwaiter().GetResult();
            _address = all.FirstOrDefault(a => a.AddressFamily == AddressFamily.InterNetwork) ?? all.FirstOrDefault();
            _host = host;
            _resolvedAt = Environment.TickCount64;
            return _address;
        }
        catch (Exception ex) when (ex is SocketException or TimeoutException or ArgumentException)
        {
            return null;
        }
    }
}
