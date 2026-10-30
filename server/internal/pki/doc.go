// Package pki — собственный CA Zafiro для агентов ПК: загрузка ключа выпускающего CA,
// выпуск клиентских сертификатов по CSR и проверка клиентских сертификатов на сервере.
//
// Выбор алгоритмов (docs/agent-protocol.md, «Реализация mTLS»):
//   - ключ CA и клиентский ключ — ECDSA P-256, подпись ECDSA-SHA256. Агент — .NET 10 на Windows:
//     SslStream (Grpc.Net.Client → SocketsHttpHandler) использует SChannel, который поддерживает
//     клиентские сертификаты ECDSA P-256/P-384 и RSA в TLS 1.2 и 1.3; CertificateRequest
//     (System.Security.Cryptography.X509Certificates) строит CSR для ECDsa и RSA; ключ CNG
//     (в том числе неэкспортируемый, в TPM через Microsoft Platform Crypto Provider) поддерживает
//     P-256 и RSA 2048. Ed25519 не подходит: ни SChannel, ни .NET (X509Certificate2/CertificateRequest)
//     не поддерживают Ed25519-сертификаты в TLS;
//   - RSA ≥ 3072 принимается как запасной вариант (как в enrollment.proto), RSA 2048 и прочие кривые —
//     нет: неверный тип ключа → ErrCSRInvalid.
//
// Ключ CA загружается из файла или секрета (PEM, путь из конфигурации), никогда из репозитория.
// В dev (Options.DevDir) при первом запуске генерируются корневой и выпускающий CA и серверный
// сертификат в указанную папку (каталог 0700, файлы 0600).
package pki
