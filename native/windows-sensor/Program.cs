using System.Net;
using System.Net.NetworkInformation;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.ServiceProcess;
using System.Text;
using System.Text.Json;
using System.Runtime.InteropServices;
using Microsoft.Diagnostics.Tracing;
using System.Buffers.Binary;
using Microsoft.Diagnostics.Tracing.Session;

namespace Nerve.Sensor;

record Configuration(string ServiceName, string ApiUrl, string Token);
record Adapter(string Id, string Name, HashSet<string> Addresses, long Metric, bool Physical);
record Flow(string LocalIp, string RemoteIp, ushort LocalPort, ushort RemotePort, string Protocol,
    string Direction, ulong Bytes, double BytesPerSecond, DateTime FirstSeenAt, DateTime LastSeenAt);

static class Program
{
    public static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web);
    public static int Main(string[] args)
    {
        if (args.SequenceEqual(new[] { "--self-test" })) return SelfTest.Run();
        if (args.SequenceEqual(new[] { "--probe" }))
        {
            // No configuration/credentials are read in this native diagnostic path.
            try { Console.WriteLine(JsonSerializer.Serialize(Interfaces.Select(), Json)); using var trace = new TraceEventSession("NERVE-Capture-Probe") { StopOnDispose = true }; trace.EnableProvider(NetworkEvents.Provider, TraceEventLevel.Informational, 0x30); Console.WriteLine("ETW session enabled."); return 0; }
            catch(Exception e) { Console.Error.WriteLine(e.GetType().Name + ": " + e.Message + " (0x" + e.HResult.ToString("X8") + ")"); return 1; }
        }
        if (args.SequenceEqual(new[] { "--schema" }))
        {
            using var metadata = new System.Diagnostics.Eventing.Reader.ProviderMetadata("Microsoft-Windows-Kernel-Network");
            foreach (var e in metadata.Events.Where(e => e.Id is 10 or 26 or 42 or 58)) Console.WriteLine(e.Id + ": " + e.Template);
            return 0;
        }
        if (args.SequenceEqual(new[] { "--interfaces" }))
        {
            Console.WriteLine(JsonSerializer.Serialize(Interfaces.Select(), Json));
            return 0;
        }
        try
        {
            // Configuration is protected by the installer, never passed on the command line.
            var config = JsonSerializer.Deserialize<Configuration>(File.ReadAllText(Path.Combine(AppContext.BaseDirectory, "sensor.json")), Json)
                ?? throw new InvalidDataException();
            if (config.Token.Length != 64 || !Uri.TryCreate(config.ApiUrl, UriKind.Absolute, out var uri)
                || uri.Scheme != "http" || uri.Host != "127.0.0.1" || uri.AbsolutePath != "/v1/snapshot")
                throw new InvalidDataException();
            ServiceBase.Run(new SensorService(config));
            return 0;
        }
        catch { Console.Error.WriteLine("Sensor startup failed. Check the protected service configuration."); return 1; }
    }
}

static class Interfaces
{
    [DllImport("iphlpapi.dll")]
    static extern uint GetIpForwardTable(IntPtr table, ref uint size, bool order);
    static Dictionary<int,long> DefaultRoutes()
    {
        uint size = 0; GetIpForwardTable(IntPtr.Zero, ref size, false);
        if (size == 0) return new();
        var buffer = Marshal.AllocHGlobal((int)size);
        try {
            if (GetIpForwardTable(buffer, ref size, false) != 0) return new();
            var result = new Dictionary<int,long>();
            for (int i=0, count=Marshal.ReadInt32(buffer); i<count; i++) {
                var row = IntPtr.Add(buffer, 4 + i * 56); // MIB_IPFORWARDROW: 14 DWORDs
                if (Marshal.ReadInt32(row,0) != 0 || Marshal.ReadInt32(row,4) != 0) continue;
                int index = Marshal.ReadInt32(row,16); long metric = (uint)Marshal.ReadInt32(row,36);
                if (!result.TryGetValue(index,out var previous) || metric < previous) result[index] = metric;
            }
            return result;
        } finally { Marshal.FreeHGlobal(buffer); }
    }
    public static Adapter Select()
    {
        var routes = DefaultRoutes();
        // Windows' IPv4 route table supplies effective default-route metrics.
        // Prefer physical Ethernet/Wi-Fi over tunnels/VM NICs.
        return NetworkInterface.GetAllNetworkInterfaces()
            .Where(n => n.OperationalStatus == OperationalStatus.Up && n.NetworkInterfaceType != NetworkInterfaceType.Loopback && n.Supports(NetworkInterfaceComponent.IPv4))
            .Select(n => {
                var ip = n.GetIPProperties();
                var addresses = ip.UnicastAddresses.Select(a => a.Address).Where(a => !IPAddress.IsLoopback(a) && !a.IsIPv6LinkLocal)
                    .Select(a => a.ToString().Split('%')[0]).ToHashSet();
                var hasGateway = ip.GatewayAddresses.Any(g => g.Address.AddressFamily == AddressFamily.InterNetwork && !g.Address.Equals(IPAddress.Any));
                var name = n.Name + " " + n.Description;
                var physical = (n.NetworkInterfaceType == NetworkInterfaceType.Ethernet || n.NetworkInterfaceType == NetworkInterfaceType.Wireless80211)
                    && !new[] { "virtual", "vethernet", "vpn", "wsl", "docker", "hyper-v", "tap-", "tunnel", "wireguard" }.Any(s => name.Contains(s, StringComparison.OrdinalIgnoreCase));
                var index = ip.GetIPv4Properties().Index;
                return (Adapter: new Adapter(n.Id, name, addresses, routes.GetValueOrDefault(index,long.MaxValue), physical), HasGateway: hasGateway && routes.ContainsKey(index));
            })
            .Where(a => a.HasGateway && a.Adapter.Addresses.Any(s => IPAddress.Parse(s).AddressFamily == AddressFamily.InterNetwork))
            .OrderByDescending(a => a.Adapter.Physical).ThenBy(a => a.Adapter.Metric).ThenBy(a => a.Adapter.Id)
            .Select(a => a.Adapter).FirstOrDefault() ?? throw new InvalidOperationException("No active host interface with an IPv4 default gateway.");
    }
}

sealed class FlowTable
{
    sealed class Counter(Flow value) { public Flow Value = value; public ulong Previous; }
    readonly Dictionary<string, Counter> flows = new();
    readonly object gate = new();
    DateTime sampled = DateTime.UtcNow;
    public bool Truncated { get; private set; }
    public static string Address(IPAddress a) => (a.IsIPv4MappedToIPv6 ? a.MapToIPv4() : a).ToString().Split('%')[0];
    public void Add(Adapter nic, IPAddress source, IPAddress destination, int sourcePort, int destinationPort, int size, string protocol, bool send, DateTime time)
    {
        if (size <= 0 || sourcePort is < 0 or > 65535 || destinationPort is < 0 or > 65535) return;
        var src = Address(source); var dst = Address(destination);
        var sourceLocal = nic.Addresses.Contains(src); var destinationLocal = nic.Addresses.Contains(dst);
        // TCP receive events can retain the connection's local-first tuple, whereas
        // UDP receive uses packet order. Establish endpoint ownership from the NIC,
        // independently of the send/receive opcode which determines byte direction.
        if (sourceLocal == destinationLocal) return;
        var local = sourceLocal ? src : dst; var remote = sourceLocal ? dst : src;
        if (IPAddress.IsLoopback(IPAddress.Parse(remote))) return;
        var lp = (ushort)(sourceLocal ? sourcePort : destinationPort); var rp = (ushort)(sourceLocal ? destinationPort : sourcePort);
        var direction = send ? "outbound" : "inbound";
        var key = $"{local}|{remote}|{lp}|{rp}|{protocol}|{direction}";
        lock (gate)
        {
            if (!flows.TryGetValue(key, out var c))
            {
                if (flows.Count >= 5000) { Truncated = true; return; }
                flows[key] = c = new(new(local, remote, lp, rp, protocol, direction, 0, 0, time, time));
            }
            c.Value = c.Value with { Bytes = c.Value.Bytes + (ulong)size, LastSeenAt = time };
        }
    }
    public Flow[] Sample(DateTime now)
    {
        lock (gate)
        {
            var seconds = Math.Max(.001, (now - sampled).TotalSeconds); sampled = now;
            foreach (var key in flows.Where(f => now - f.Value.Value.LastSeenAt > TimeSpan.FromSeconds(15)).Select(f => f.Key).ToArray()) flows.Remove(key);
            var result = flows.Values.Select(c => {
                var value = c.Value with { BytesPerSecond = (c.Value.Bytes - c.Previous) / seconds };
                c.Previous = c.Value.Bytes; return value;
            }).ToArray();
            return result;
        }
    }
}

static class NetworkEvents
{
    // Manifest provider shipped with Windows. A regular real-time ETW session
    // avoids the privileged legacy/system kernel logger and unrelated process events.
    public static readonly Guid Provider = new("7dd42a49-5329-4832-8dfd-43d979153a88");
    public static void Accept(TraceEvent e, Adapter nic, FlowTable table)
    {
        if (e.ProviderGuid != Provider || e.Version != 0) return;
        int id = (int)e.ID;
        if (id is not (10 or 11 or 26 or 27 or 42 or 43 or 58 or 59)) return;
        bool ipv6 = id is 26 or 27 or 58 or 59;
        var data = e.EventData().AsSpan();
        int width = ipv6 ? 16 : 4, ports = 8 + 2 * width;
        if (data.Length < ports + 4) return;
        // Native manifest: PID:u32, size:u32, daddr, saddr, dport, sport.
        // Addresses/ports use network byte order; size is a little-endian DWORD.
        var destination = new IPAddress(data.Slice(8,width));
        var source = new IPAddress(data.Slice(8+width,width));
        table.Add(nic, source, destination, BinaryPrimitives.ReadUInt16BigEndian(data.Slice(ports+2,2)),
            BinaryPrimitives.ReadUInt16BigEndian(data.Slice(ports,2)), BinaryPrimitives.ReadInt32LittleEndian(data.Slice(4,4)),
            id < 40 ? "TCP" : "UDP", id is 10 or 26 or 42 or 58, e.TimeStamp.ToUniversalTime());
    }
}

sealed class SensorService : ServiceBase
{
    readonly Configuration config;
    readonly CancellationTokenSource stop = new();
    Task? worker;
    TraceEventSession? session;
    public SensorService(Configuration configuration) { config = configuration; ServiceName = config.ServiceName; CanStop = true; AutoLog = false; }
    protected override void OnStart(string[] args) { worker = Task.Run(Run); }
    protected override void OnStop()
    {
        stop.Cancel(); session?.Dispose();
        try { worker?.Wait(TimeSpan.FromSeconds(8)); } catch { }
    }
    async Task Run()
    {
        using var client = new HttpClient(new HttpClientHandler { AllowAutoRedirect = false, UseProxy = false }) { Timeout = TimeSpan.FromSeconds(4) };
        while (!stop.IsCancellationRequested)
        {
            var stage = "interface_selection";
            try
            {
                var nic = Interfaces.Select(); var table = new FlowTable();
                stage = "etw_session";
                using var trace = new TraceEventSession(config.ServiceName) { StopOnDispose = true, BufferSizeMB = 16 };
                session = trace;
                trace.EnableProvider(NetworkEvents.Provider, TraceEventLevel.Informational, 0x30);
                trace.Source.Dynamic.All += e => NetworkEvents.Accept(e, nic, table);
                stage = "etw_processing";
                var processing = Task.Run(() => trace.Source.Process());
                try
                {
                    while (!stop.IsCancellationRequested && !processing.IsCompleted)
                    {
                        var now = DateTime.UtcNow;
                        await Publish(client, new { version = 1, generatedAt = now, status = "connected", interfaceId = nic.Id,
                            interfaceName = nic.Name, localAddresses = nic.Addresses, flows = table.Sample(now),
                            truncated = table.Truncated, eventsLost = trace.Source.EventsLost });
                        await Task.Delay(2000, stop.Token);
                        var current = Interfaces.Select();
                        if (current.Id != nic.Id || !current.Addresses.SetEquals(nic.Addresses)) break;
                    }
                }
                finally { trace.Dispose(); await processing; session = null; }
            }
            catch (OperationCanceledException) when (stop.IsCancellationRequested) { break; }
            catch (Exception e)
            {
                // Never serialize exceptions (they can contain URLs/configuration).
                var denied = e is UnauthorizedAccessException || e is System.Security.SecurityException || (e is System.ComponentModel.Win32Exception win32 && win32.NativeErrorCode == 5);
                var status = denied ? "capture_permission_denied" : "capture_backend_unavailable";
                await Publish(client, new { version = 1, generatedAt = DateTime.UtcNow, status, backendStage = stage, backendError = e.HResult, flows = Array.Empty<Flow>() });
                try { await Task.Delay(5000, stop.Token); } catch (OperationCanceledException) { break; }
            }
        }
    }
    async Task Publish(HttpClient client, object payload)
    {
        var body = JsonSerializer.SerializeToUtf8Bytes(payload, Program.Json);
        var timestamp = DateTimeOffset.UtcNow.ToUnixTimeSeconds().ToString(System.Globalization.CultureInfo.InvariantCulture);
        var nonce = Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();
        var prefix = Encoding.UTF8.GetBytes(timestamp + "\n" + nonce + "\n");
        using var mac = IncrementalHash.CreateHMAC(HashAlgorithmName.SHA256, Encoding.UTF8.GetBytes(config.Token));
        mac.AppendData(prefix); mac.AppendData(body);
        using var request = new HttpRequestMessage(HttpMethod.Post, config.ApiUrl) { Content = new ByteArrayContent(body) };
        request.Content.Headers.ContentType = new("application/json");
        request.Headers.Add("X-Nerve-Time", timestamp); request.Headers.Add("X-Nerve-Nonce", nonce);
        request.Headers.Add("X-Nerve-Signature", Convert.ToHexString(mac.GetHashAndReset()).ToLowerInvariant());
        try { using var response = await client.SendAsync(request, stop.Token); }
        catch (HttpRequestException) { } catch (TaskCanceledException) { }
    }
}

static class SelfTest
{
    public static int Run()
    {
        var table = new FlowTable(); var now = DateTime.UtcNow;
        var nic = new Adapter("test", "test", new() { "192.168.1.2", "2001:db8::1" }, 1, true);
        table.Add(nic, IPAddress.Parse("192.168.1.2"), IPAddress.Parse("1.1.1.1"), 50001, 443, 100, "TCP", true, now);
        table.Add(nic, IPAddress.Parse("1.1.1.1"), IPAddress.Parse("192.168.1.2"), 443, 50001, 200, "TCP", false, now);
        table.Add(nic, IPAddress.Parse("192.168.1.2"), IPAddress.Parse("1.1.1.1"), 50001, 443, 75, "TCP", false, now);
        table.Add(nic, IPAddress.Parse("2001:db8::1"), IPAddress.Parse("2606:4700::1111"), 50002, 53, 50, "UDP", true, now);
        table.Add(nic, IPAddress.Parse("172.17.0.2"), IPAddress.Parse("1.1.1.1"), 1, 2, 999, "TCP", true, now);
        var flows = table.Sample(now.AddSeconds(2));
        if (flows.Length != 3 || flows.Sum(f => (long)f.Bytes) != 425 || flows.Single(f => f.Direction == "inbound").LocalPort != 50001) return 1;
        if (table.Sample(now.AddSeconds(4)).Any(f => f.BytesPerSecond != 0) || table.Sample(now.AddSeconds(20)).Length != 0) return 1;
        Console.WriteLine("PASS: IPv4/IPv6 TCP/UDP normalization, direction, adapter filter, delta throughput and expiry (synthetic unit input)."); return 0;
    }
}
