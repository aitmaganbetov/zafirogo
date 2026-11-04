package enroll

import (
	"context"
	"errors"
	"net/netip"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
)

// GRPC — EnrollmentService поверх Service (регистрируется grpcserver.NewServer через Options.Enrollment).
type GRPC struct {
	agentv1.UnimplementedEnrollmentServiceServer
	svc *Service
}

// NewGRPC — реализация EnrollmentServiceServer.
func NewGRPC(svc *Service) *GRPC { return &GRPC{svc: svc} }

// Enroll — без клиентского сертификата (grpcserver.MethodEnroll в списке публичных).
func (g *GRPC) Enroll(ctx context.Context, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	resp, err := g.svc.Enroll(ctx, EnrollInput{
		Code: req.GetEnrollmentCode(), ImageKey: req.GetImageKey(), CSRPEM: req.GetCsrPem(), Device: req.GetDevice(),
		IP: peerAddr(ctx),
	})
	if err != nil {
		return nil, g.toStatus(ctx, "Enroll", err)
	}
	return resp, nil
}

// RenewCertificate — под mTLS (идентичность ПК — из сертификата).
func (g *GRPC) RenewCertificate(ctx context.Context, req *agentv1.RenewCertificateRequest) (*agentv1.RenewCertificateResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, ToGRPC(ErrNoIdentity)
	}
	current, err := grpcserver.VerifiedPeerCertificate(ctx)
	if err != nil {
		return nil, ToGRPC(ErrNoIdentity)
	}
	resp, err := g.svc.RenewCertificate(ctx, id, RenewInput{
		CSRPEM: req.GetCsrPem(), Device: req.GetDevice(), Current: current, IP: peerAddr(ctx),
	})
	if err != nil {
		return nil, g.toStatus(ctx, "RenewCertificate", err)
	}
	return resp, nil
}

func (g *GRPC) toStatus(ctx context.Context, method string, err error) error {
	var e *Error
	if !errors.As(err, &e) {
		g.svc.opts.Logger.ErrorContext(ctx, "enrollment rpc failed", "method", method, "error", err)
	}
	return ToGRPC(err)
}

func peerAddr(ctx context.Context) netip.Addr {
	ip := grpcserver.PeerIP(ctx)
	if ip == nil {
		return netip.Addr{}
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}
	}
	return a.Unmap()
}
