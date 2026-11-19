using Google.Protobuf;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Persistence;

/// <summary>
/// Тема клуба на диске службы (shell-theme.pb): после перезагрузки ПК экран входа сразу в цветах клуба, ещё до Welcome
/// (старый агент хранил её в AgentConfig.ShellTheme). Стандартная тема — файла нет. Пишется только при смене темы.
/// </summary>
public sealed class ShellThemeStore
{
    private readonly object _gate = new();
    private readonly string? _path;
    private ShellTheme? _saved;

    /// <param name="path">Файл темы; null — только в памяти (тесты).</param>
    public ShellThemeStore(string? path)
    {
        _path = path;
        _saved = Read(path);
    }

    /// <summary>Сохранённая тема (null — стандартная или файла нет/испорчен).</summary>
    public ShellTheme? Saved
    {
        get
        {
            lock (_gate) return _saved?.Clone();
        }
    }

    /// <summary>Запоминает тему, если она другая. true — файл изменён.</summary>
    public bool Save(ShellTheme? theme)
    {
        lock (_gate)
        {
            if (theme is null ? _saved is null : theme.Equals(_saved)) return false;
            if (_path is not null)
            {
                if (theme is null) File.Delete(_path);
                else AtomicFile.Write(_path, theme.ToByteArray());
            }

            _saved = theme?.Clone();
            return true;
        }
    }

    private static ShellTheme? Read(string? path)
    {
        if (path is null) return null;
        var bytes = AtomicFile.Read(path);
        if (bytes is null) return null;
        try
        {
            return ShellTheme.Parser.ParseFrom(bytes);
        }
        catch (InvalidProtocolBufferException)
        {
            return null; // испорчен — стандартная тема до Welcome
        }
    }
}
