namespace Zafiro.Agent.Core.Persistence;

/// <summary>
/// Атомарная запись файлов состояния: временный файл → fsync → переименование. Сбой питания посреди записи
/// оставляет прежнюю версию, а не обрывок. На Unix файлы создаются с правами 0600, каталог — 0700.
/// </summary>
public static class AtomicFile
{
    private const UnixFileMode FileMode600 = UnixFileMode.UserRead | UnixFileMode.UserWrite;
    private const UnixFileMode DirMode700 = UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.UserExecute;

    /// <summary>Создаёт каталог (на Unix — 0700; существующий приводится к 0700).</summary>
    public static void EnsureDirectory(string path)
    {
        if (OperatingSystem.IsWindows())
        {
            Directory.CreateDirectory(path);
            return;
        }

        if (!Directory.Exists(path)) Directory.CreateDirectory(path, DirMode700);
        else File.SetUnixFileMode(path, DirMode700);
    }

    /// <summary>Записывает байты атомарно.</summary>
    public static void Write(string path, ReadOnlySpan<byte> data)
    {
        var dir = Path.GetDirectoryName(Path.GetFullPath(path))!;
        EnsureDirectory(dir);
        var temp = path + ".tmp";
        var options = new FileStreamOptions
        {
            Mode = FileMode.Create,
            Access = FileAccess.Write,
            Share = FileShare.None,
            Options = FileOptions.WriteThrough,
        };
        if (!OperatingSystem.IsWindows()) options.UnixCreateMode = FileMode600;
        using (var fs = new FileStream(temp, options))
        {
            fs.Write(data);
            fs.Flush(flushToDisk: true);
        }

        if (!OperatingSystem.IsWindows()) File.SetUnixFileMode(temp, FileMode600);
        File.Move(temp, path, overwrite: true);
    }

    /// <summary>Читает файл; null — файла нет.</summary>
    public static byte[]? Read(string path)
    {
        try
        {
            return File.ReadAllBytes(path);
        }
        catch (FileNotFoundException)
        {
            return null;
        }
        catch (DirectoryNotFoundException)
        {
            return null;
        }
    }

    /// <summary>Удаляет файл, если он есть.</summary>
    public static void Delete(string path)
    {
        try
        {
            File.Delete(path);
        }
        catch (DirectoryNotFoundException)
        {
        }
    }
}
