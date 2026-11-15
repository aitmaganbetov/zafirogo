using System.Runtime.InteropServices;

// Все P/Invoke сборки — системные библиотеки Windows (kernel32, user32, advapi32, ole32, oleaut32, setupapi, shell32,
// userenv, wevtapi, wtsapi32): искать их только в System32. Без этого библиотеки вне списка KnownDLLs (wevtapi,
// wtsapi32, userenv) сначала ищутся в папке программы — подложенная туда DLL загрузилась бы в процесс службы
// (LocalSystem). Аудит безопасности A-08. nvml.dll (NativeLibrary.TryLoad по абсолютному пути) и WebView2Loader
// (его грузит сборка Microsoft.Web.WebView2.Core) под действие атрибута не попадают.
[assembly: DefaultDllImportSearchPaths(DllImportSearchPath.System32)]
