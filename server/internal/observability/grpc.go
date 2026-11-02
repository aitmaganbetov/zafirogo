package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor — метрики, спан и request id unary-вызова. Ставится внешним (до проверки сертификата,
// grpcserver.Options.OuterUnary): отказы UNAUTHENTICATED тоже считаются.
func (o *Observer) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if o == nil {
			return handler(ctx, req)
		}
		start := time.Now()
		method := o.g.method.value(info.FullMethod)
		ctx = WithRequestID(ctx, NewRequestID())
		ctx, span := o.tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("rpc.system", "grpc"), attribute.String("rpc.method", method)))
		resp, err := handler(ctx, req)
		code := status.Code(err)
		o.m.grpcRequests.WithLabelValues(method, code.String()).Inc()
		o.m.grpcDuration.WithLabelValues(method).Observe(time.Since(start).Seconds())
		if code == codes.ResourceExhausted {
			o.m.rateLimited.WithLabelValues("grpc", method).Inc()
		}
		span.SetAttributes(attribute.String("rpc.grpc.status_code", code.String()))
		if isServerError(code) {
			span.SetStatus(otelcodes.Error, code.String())
		}
		span.End()
		return resp, err
	}
}

// StreamServerInterceptor — потоки: активные потоки по методу, входящие сообщения, итоговый код. Спан на поток не
// создаётся: Connect агента живёт часами, такой спан бесполезен и держит память.
func (o *Observer) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if o == nil {
			return handler(srv, ss)
		}
		method := o.g.method.value(info.FullMethod)
		active := o.m.grpcStreamsActive.WithLabelValues(method)
		active.Inc()
		defer active.Dec()
		ctx := WithRequestID(ss.Context(), NewRequestID())
		err := handler(srv, &observedStream{ServerStream: ss, ctx: ctx, msgs: o.m.grpcStreamMsgs.WithLabelValues(method)})
		code := status.Code(err)
		o.m.grpcRequests.WithLabelValues(method, code.String()).Inc()
		if code == codes.ResourceExhausted {
			o.m.rateLimited.WithLabelValues("grpc", method).Inc()
		}
		return err
	}
}

type observedStream struct {
	grpc.ServerStream
	ctx  context.Context
	msgs interface{ Inc() }
}

func (s *observedStream) Context() context.Context { return s.ctx }

func (s *observedStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.msgs.Inc()
	}
	return err
}

// isServerError — коды, которые считаются ошибкой сервера (как 5xx в HTTP; monitoring.grpcStatus — то же).
func isServerError(c codes.Code) bool {
	switch c {
	case codes.Internal, codes.Unknown, codes.Unavailable, codes.DataLoss, codes.DeadlineExceeded:
		return true
	}
	return false
}
