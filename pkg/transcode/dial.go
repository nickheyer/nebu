package transcode

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// The application protocol a model server port declares
type AppProtocol string

// Kubernetes h2c and mesh grpc appProtocol values, both cleartext HTTP/2 with prior knowledge
const (
	H2C  AppProtocol = "kubernetes.io/h2c"
	GRPC AppProtocol = "grpc"
)

// Interval between client keepalive pings on an idle connection
const KeepaliveTime = 30 * time.Second

// Largest single backend message, sized for large embedding and logprob answers
const MaxRecvBytes = 256 << 20

// Opens a cleartext HTTP/2 gRPC connection with keepalive to a port declaring h2c or grpc
func Dial(addr string, proto AppProtocol) (*grpc.ClientConn, error) {
	return DialWith(addr, proto, nil)
}

// Opens a gRPC connection through a dialer, the connection's bytes unchanged, plainly when the dialer is nil
func DialWith(addr string, proto AppProtocol, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*grpc.ClientConn, error) {
	if proto != H2C && proto != GRPC {
		return nil, fmt.Errorf("transcode: %s declares app protocol %q, gRPC needs %q or %q", addr, proto, H2C, GRPC)
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                KeepaliveTime,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxRecvBytes)),
	}
	if dial != nil {
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, target string) (net.Conn, error) { return dial(ctx, "tcp", target) }))
	}
	return grpc.NewClient(addr, opts...)
}

// Bounds one call by timeout while keeping any earlier caller deadline, zero means no bound
func CallContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
