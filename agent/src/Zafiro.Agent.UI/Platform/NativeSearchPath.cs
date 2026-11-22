using System.Runtime.InteropServices;

// Все P/Invoke сборки UI — системные библиотеки Windows (kernel32, user32, shell32): искать их только в System32, как в
// службе (Zafiro.Agent.Service/Platform/NativeSearchPath.cs) — подложенная в папку программы DLL не загрузится.
// Аудит безопасности A-08. WebView2Loader (его грузит сборка Microsoft.Web.WebView2.Core) под действие атрибута не попадает.
[assembly: DefaultDllImportSearchPaths(DllImportSearchPath.System32)]
