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
        // Испорченный файл — стандартная тема до Welcome.
        _saved = path is null ? null : AtomicFile.ReadMessage(path, ShellTheme.Parser);
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
}
