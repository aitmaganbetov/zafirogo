using Google.Protobuf.Reflection;
using Google.Rpc;
using Grpc.Core;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Errors;

/// <summary>Нарушение поля из google.rpc.BadRequest.</summary>
public sealed record FieldViolationInfo(string Field, string Description);

/// <summary>
/// Ошибка вызова сервера, разобранная из google.rpc.Status (docs/agent-protocol.md §7): gRPC-код,
/// <see cref="ErrorCode"/> из ErrorInfo.reason (домен agent.zafiro.kz), текст для показа (LocalizedMessage),
/// RetryInfo и BadRequest. Агент решает, что делать, по <see cref="ErrorCode"/>, а не по тексту.
/// </summary>
public class AgentRpcException : Exception
{
    /// <summary>ErrorInfo.domain ошибок контракта агента.</summary>
    public const string Domain = "agent.zafiro.kz";

    public AgentRpcException(StatusCode statusCode, ErrorCode errorCode, string? localizedMessage, string? detail,
        TimeSpan? retryAfter, IReadOnlyDictionary<string, string> metadata, IReadOnlyList<FieldViolationInfo> violations,
        Exception? inner)
        : base(BuildMessage(statusCode, errorCode, localizedMessage ?? detail), inner)
    {
        StatusCode = statusCode;
        ErrorCode = errorCode;
        LocalizedMessage = localizedMessage;
        Detail = detail;
        RetryAfter = retryAfter;
        Metadata = metadata;
        FieldViolations = violations;
    }

    /// <summary>gRPC-код.</summary>
    public StatusCode StatusCode { get; }

    /// <summary>Код контракта; неизвестный reason — <see cref="ErrorCode.Internal"/> (common.proto), без ErrorInfo — Unspecified.</summary>
    public ErrorCode ErrorCode { get; }

    /// <summary>Текст для человека (google.rpc.LocalizedMessage) — показывается как есть; null — сервер не прислал.</summary>
    public string? LocalizedMessage { get; }

    /// <summary>Текст статуса gRPC (для журнала, не для гостя).</summary>
    public string? Detail { get; }

    /// <summary>google.rpc.RetryInfo.retry_delay (или ErrorInfo.metadata.retry_after_seconds).</summary>
    public TimeSpan? RetryAfter { get; }

    /// <summary>ErrorInfo.metadata.</summary>
    public IReadOnlyDictionary<string, string> Metadata { get; }

    /// <summary>google.rpc.BadRequest.field_violations.</summary>
    public IReadOnlyList<FieldViolationInfo> FieldViolations { get; }

    /// <summary>
    /// Повторяемая ошибка транспорта (agent.proto п. 8): UNAVAILABLE, RESOURCE_EXHAUSTED, ABORTED, DEADLINE_EXCEEDED,
    /// INTERNAL; кроме фатальных кодов сертификата и версии протокола.
    /// </summary>
    public bool IsRetryable => ErrorCode switch
    {
        ErrorCode.CertificateRevoked or ErrorCode.CertificateExpired or ErrorCode.CertificateRequired => false,
        ErrorCode.IdempotencyKeyReused or ErrorCode.ValidationFailed => false,
        _ => StatusCode is StatusCode.Unavailable or StatusCode.ResourceExhausted or StatusCode.Aborted
            or StatusCode.DeadlineExceeded or StatusCode.Internal or StatusCode.Unknown,
    };

    private static string BuildMessage(StatusCode code, ErrorCode errorCode, string? text) =>
        $"{code} {ErrorCodes.Name(errorCode)}" + (string.IsNullOrEmpty(text) ? "" : ": " + text);
}

/// <summary>Сертификат ПК отозван: ключ и сертификат удаляются, нужен новый код регистрации.</summary>
public sealed class CertificateRevokedException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Сертификат истёк или не предъявлен (CERTIFICATE_EXPIRED / CERTIFICATE_REQUIRED): нужна регистрация (Enroll).</summary>
public sealed class CertificateExpiredException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Бездисковый ПК ждёт подтверждения в панели клуба (PENDING_APPROVAL + RetryInfo).</summary>
public sealed class PendingApprovalException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Лимит частоты (RATE_LIMITED + RetryInfo).</summary>
public sealed class RateLimitedException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Токен гостя недействителен: агент выполняет выход гостя.</summary>
public sealed class GuestTokenInvalidException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Ошибка валидации полей (VALIDATION_FAILED + BadRequest).</summary>
public sealed class ValidationFailedException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Версия протокола не поддерживается сервером: переподключение не чаще раза в 10 минут.</summary>
public sealed class ProtocolVersionUnsupportedException(AgentRpcException e) : AgentRpcExceptionWrapper(e);

/// <summary>Базовый класс типизированных ошибок: те же свойства, что у исходной.</summary>
public abstract class AgentRpcExceptionWrapper(AgentRpcException e)
    : AgentRpcException(e.StatusCode, e.ErrorCode, e.LocalizedMessage, e.Detail, e.RetryAfter, e.Metadata, e.FieldViolations, e.InnerException);

/// <summary>Разбор <see cref="RpcException"/> в типизированные ошибки контракта.</summary>
public static class RpcErrors
{
    private static readonly EnumDescriptor ErrorCodeDescriptor =
        CommonReflection.Descriptor.EnumTypes.First(e => e.Name == nameof(ErrorCode));

    /// <summary>Типизированное исключение по ErrorInfo.reason.</summary>
    public static AgentRpcException Parse(RpcException ex)
    {
        ArgumentNullException.ThrowIfNull(ex);
        Google.Rpc.Status? st = null;
        try
        {
            st = ex.GetRpcStatus();
        }
        catch (Google.Protobuf.InvalidProtocolBufferException)
        {
            // Испорченный grpc-status-details-bin — ошибка без деталей.
        }

        var errorCode = ErrorCode.Unspecified;
        string? localized = null;
        TimeSpan? retryAfter = null;
        var metadata = new Dictionary<string, string>(StringComparer.Ordinal);
        var violations = new List<FieldViolationInfo>();
        if (st is not null)
        {
            foreach (var any in st.Details)
            {
                if (any.TryUnpack(out ErrorInfo info))
                {
                    if (info.Domain == AgentRpcException.Domain || string.IsNullOrEmpty(info.Domain))
                    {
                        errorCode = ParseReason(info.Reason);
                    }

                    foreach (var kv in info.Metadata) metadata[kv.Key] = kv.Value;
                }
                else if (any.TryUnpack(out LocalizedMessage lm))
                {
                    localized ??= lm.Message;
                }
                else if (any.TryUnpack(out RetryInfo ri) && ri.RetryDelay is not null)
                {
                    retryAfter = ri.RetryDelay.ToTimeSpan();
                }
                else if (any.TryUnpack(out BadRequest br))
                {
                    violations.AddRange(br.FieldViolations.Select(v => new FieldViolationInfo(v.Field, v.Description)));
                }
            }
        }

        if (retryAfter is null && metadata.TryGetValue("retry_after_seconds", out var secs)
            && long.TryParse(secs, System.Globalization.NumberStyles.None, System.Globalization.CultureInfo.InvariantCulture, out var s) && s > 0)
        {
            retryAfter = TimeSpan.FromSeconds(s);
        }

        var basic = new AgentRpcException(ex.StatusCode, errorCode, localized, ex.Status.Detail, retryAfter, metadata, violations, ex);
        return errorCode switch
        {
            ErrorCode.CertificateRevoked => new CertificateRevokedException(basic),
            ErrorCode.CertificateExpired or ErrorCode.CertificateRequired => new CertificateExpiredException(basic),
            ErrorCode.PendingApproval => new PendingApprovalException(basic),
            ErrorCode.RateLimited => new RateLimitedException(basic),
            ErrorCode.GuestTokenInvalid => new GuestTokenInvalidException(basic),
            ErrorCode.ValidationFailed => new ValidationFailedException(basic),
            ErrorCode.ProtocolVersionUnsupported => new ProtocolVersionUnsupportedException(basic),
            _ => basic,
        };
    }

    /// <summary>ErrorInfo.reason ("ERROR_CODE_…") → <see cref="ErrorCode"/>; неизвестное — Internal.</summary>
    public static ErrorCode ParseReason(string? reason)
    {
        if (string.IsNullOrEmpty(reason)) return ErrorCode.Unspecified;
        var v = ErrorCodeDescriptor.FindValueByName(reason);
        return v is null ? ErrorCode.Internal : (ErrorCode)v.Number;
    }

    /// <summary>Выполняет вызов и переводит <see cref="RpcException"/> в типизированную ошибку.</summary>
    public static async Task<T> Call<T>(Func<Task<T>> call)
    {
        ArgumentNullException.ThrowIfNull(call);
        try
        {
            return await call().ConfigureAwait(false);
        }
        catch (RpcException ex)
        {
            throw Parse(ex);
        }
    }
}

/// <summary>Имена значений ErrorCode как в proto ("ERROR_CODE_INSUFFICIENT_FUNDS").</summary>
public static class ErrorCodes
{
    private static readonly EnumDescriptor Descriptor =
        CommonReflection.Descriptor.EnumTypes.First(e => e.Name == nameof(ErrorCode));

    public static string Name(ErrorCode code) => Descriptor.FindValueByNumber((int)code)?.Name ?? ((int)code).ToString(System.Globalization.CultureInfo.InvariantCulture);
}
