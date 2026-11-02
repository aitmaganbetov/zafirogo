using System.Buffers.Binary;
using Google.Protobuf;
using Zafiro.Agent.Ipc.V1;

namespace Zafiro.Agent.Ipc;

/// <summary>Кадры IPC: 4 байта длины (little-endian) + Envelope (protobuf), не больше 1 МиБ.</summary>
public static class IpcFraming
{
    public const int MaxFrameBytes = 1024 * 1024;

    /// <summary>Читает кадр; null — канал закрыт. Слишком большой или испорченный кадр — <see cref="InvalidDataException"/>.</summary>
    public static async Task<Envelope?> ReadAsync(Stream stream, CancellationToken ct)
    {
        ArgumentNullException.ThrowIfNull(stream);
        var header = new byte[4];
        if (!await ReadExactly(stream, header, ct).ConfigureAwait(false)) return null;
        var length = BinaryPrimitives.ReadInt32LittleEndian(header);
        if (length < 0 || length > MaxFrameBytes) throw new InvalidDataException("IPC frame is too large.");
        var body = new byte[length];
        if (!await ReadExactly(stream, body, ct).ConfigureAwait(false)) throw new InvalidDataException("IPC frame is truncated.");
        try
        {
            return Envelope.Parser.ParseFrom(body);
        }
        catch (InvalidProtocolBufferException e)
        {
            throw new InvalidDataException("IPC frame is malformed.", e);
        }
    }

    /// <summary>Пишет кадр (вызывающий сериализует запись в один поток).</summary>
    public static async Task WriteAsync(Stream stream, Envelope envelope, CancellationToken ct)
    {
        ArgumentNullException.ThrowIfNull(stream);
        ArgumentNullException.ThrowIfNull(envelope);
        var size = envelope.CalculateSize();
        if (size > MaxFrameBytes) throw new InvalidDataException("IPC frame is too large.");
        var buf = new byte[4 + size];
        BinaryPrimitives.WriteInt32LittleEndian(buf, size);
        envelope.WriteTo(buf.AsSpan(4));
        await stream.WriteAsync(buf, ct).ConfigureAwait(false);
        await stream.FlushAsync(ct).ConfigureAwait(false);
    }

    private static async Task<bool> ReadExactly(Stream stream, byte[] buffer, CancellationToken ct)
    {
        var read = 0;
        while (read < buffer.Length)
        {
            var n = await stream.ReadAsync(buffer.AsMemory(read), ct).ConfigureAwait(false);
            if (n == 0) return read == 0 && buffer.Length > 0 ? false : throw new EndOfStreamException();
            read += n;
        }

        return true;
    }
}
