using Google.Protobuf.WellKnownTypes;
using Google.Rpc;
using Grpc.Core;
using Zafiro.Agent.Core.Errors;
using Zafiro.Agent.V1;
using RpcStatus = Google.Rpc.Status;

namespace Zafiro.Agent.Tests;

/// <summary>Разбор google.rpc.Status так, как его собирает grpcserver.Error / RateLimited / Validation сервера.</summary>
public class RpcErrorsTests
{
    private static RpcException Build(Code code, string reason, string? text = null, IDictionary<string, string>? metadata = null, params Google.Protobuf.IMessage[] extra)
    {
        var st = new RpcStatus { Code = (int)code, Message = text ?? "" };
        var info = new ErrorInfo { Reason = reason, Domain = AgentRpcException.Domain };
        if (metadata is not null) info.Metadata.Add(metadata);
        st.Details.Add(Any.Pack(info));
        if (text is not null) st.Details.Add(Any.Pack(new LocalizedMessage { Locale = "ru", Message = text }));
        foreach (var e in extra) st.Details.Add(Any.Pack(e));
        return st.ToRpcException();
    }

    [Fact]
    public void Бизнес_ошибка_с_текстом_для_гостя()
    {
        var e = RpcErrors.Parse(Build(Code.FailedPrecondition, "ERROR_CODE_INSUFFICIENT_FUNDS", "Недостаточно средств."));
        Assert.IsType<AgentRpcException>(e);
        Assert.Equal(StatusCode.FailedPrecondition, e.StatusCode);
        Assert.Equal(ErrorCode.InsufficientFunds, e.ErrorCode);
        Assert.Equal("Недостаточно средств.", e.LocalizedMessage);
        Assert.False(e.IsRetryable);
    }

    [Fact]
    public void Отзыв_сертификата_типизирован_и_не_повторяется()
    {
        var e = RpcErrors.Parse(Build(Code.Unauthenticated, "ERROR_CODE_CERTIFICATE_REVOKED", "ПК отключён."));
        Assert.IsType<CertificateRevokedException>(e);
        Assert.False(e.IsRetryable);
        Assert.IsType<CertificateExpiredException>(RpcErrors.Parse(Build(Code.Unauthenticated, "ERROR_CODE_CERTIFICATE_REQUIRED")));
        Assert.IsType<CertificateExpiredException>(RpcErrors.Parse(Build(Code.Unauthenticated, "ERROR_CODE_CERTIFICATE_EXPIRED")));
    }

    [Fact]
    public void Лимит_с_RetryInfo()
    {
        var e = RpcErrors.Parse(Build(Code.ResourceExhausted, "ERROR_CODE_RATE_LIMITED", "Слишком много попыток.",
            new Dictionary<string, string> { ["retry_after_seconds"] = "42" },
            new RetryInfo { RetryDelay = Duration.FromTimeSpan(TimeSpan.FromSeconds(42)) }));
        Assert.IsType<RateLimitedException>(e);
        Assert.Equal(TimeSpan.FromSeconds(42), e.RetryAfter);
        Assert.True(e.IsRetryable);
    }

    [Fact]
    public void Ожидание_подтверждения_бездискового_ПК()
    {
        var e = RpcErrors.Parse(Build(Code.FailedPrecondition, "ERROR_CODE_PENDING_APPROVAL", "ПК ждёт подтверждения.",
            new Dictionary<string, string> { ["retry_after_seconds"] = "30" },
            new RetryInfo { RetryDelay = Duration.FromTimeSpan(TimeSpan.FromSeconds(30)) }));
        Assert.IsType<PendingApprovalException>(e);
        Assert.Equal(TimeSpan.FromSeconds(30), e.RetryAfter);
    }

    [Fact]
    public void Retry_after_seconds_без_RetryInfo()
    {
        var e = RpcErrors.Parse(Build(Code.FailedPrecondition, "ERROR_CODE_PENDING_APPROVAL", null, new Dictionary<string, string> { ["retry_after_seconds"] = "15" }));
        Assert.Equal(TimeSpan.FromSeconds(15), e.RetryAfter);
    }

    [Fact]
    public void Валидация_с_полями()
    {
        var e = RpcErrors.Parse(Build(Code.InvalidArgument, "ERROR_CODE_VALIDATION_FAILED", "Неверный телефон.",
            new Dictionary<string, string> { ["field"] = "phone" },
            new BadRequest { FieldViolations = { new BadRequest.Types.FieldViolation { Field = "phone", Description = "Неверный телефон." } } }));
        var v = Assert.IsType<ValidationFailedException>(e);
        Assert.Equal("phone", Assert.Single(v.FieldViolations).Field);
        Assert.Equal("phone", v.Metadata["field"]);
    }

    [Fact]
    public void Неизвестный_reason_трактуется_как_INTERNAL()
    {
        var e = RpcErrors.Parse(Build(Code.FailedPrecondition, "ERROR_CODE_FROM_THE_FUTURE"));
        Assert.Equal(ErrorCode.Internal, e.ErrorCode);
    }

    [Fact]
    public void Без_деталей_только_код_транспорта()
    {
        var e = RpcErrors.Parse(new RpcException(new Grpc.Core.Status(StatusCode.Unavailable, "connection refused")));
        Assert.Equal(ErrorCode.Unspecified, e.ErrorCode);
        Assert.Null(e.LocalizedMessage);
        Assert.True(e.IsRetryable);
        Assert.Equal("connection refused", e.Detail);
    }

    [Fact]
    public void Испорченные_детали_не_роняют_разбор()
    {
        var trailers = new Metadata { { "grpc-status-details-bin", new byte[] { 0xFF, 0x01, 0x02 } } };
        var e = RpcErrors.Parse(new RpcException(new Grpc.Core.Status(StatusCode.Internal, "x"), trailers));
        Assert.Equal(StatusCode.Internal, e.StatusCode);
    }

    [Fact]
    public void Имена_кодов_как_в_proto()
    {
        Assert.Equal("ERROR_CODE_GUEST_TOKEN_INVALID", ErrorCodes.Name(ErrorCode.GuestTokenInvalid));
        Assert.Equal(ErrorCode.CertificateRequired, RpcErrors.ParseReason("ERROR_CODE_CERTIFICATE_REQUIRED"));
        Assert.Equal(ErrorCode.Unspecified, RpcErrors.ParseReason(""));
    }
}
