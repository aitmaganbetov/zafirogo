using System.Diagnostics;

namespace Zafiro.Agent.UI;

/// <summary>
/// Журнал процесса UI — только трассировка Windows (DebugView): у UI нет своей папки данных, а секретов он не видит
/// (пароли гостя уходят в службу внутри запроса и не пишутся никуда).
/// </summary>
internal static class UiLog
{
    public static void Write(string message) => Trace.WriteLine("[Zafiro.Agent.UI] " + message);
}
