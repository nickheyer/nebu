package instances

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// The ggml rpc protocol's hello command: a one byte command, an eight byte input size naming the
// capability block that follows, 24 bytes, all zero when the client offers no RDMA, answered
// with an eight byte size and the server's protocol version in the first three bytes
const (
	rpcCmdHello     = 14
	rpcHelloCaps    = 24
	rpcHelloTimeout = 3 * time.Second
)

// Makes one hello exchange with a ggml rpc server and returns the protocol version it speaks
func rpcHello(ctx context.Context, addr string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcHelloTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	req := make([]byte, 9+rpcHelloCaps)
	req[0] = rpcCmdHello
	binary.LittleEndian.PutUint64(req[1:9], rpcHelloCaps)
	if _, err := conn.Write(req); err != nil {
		return "", err
	}
	var size [8]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return "", fmt.Errorf("no hello answer: %w", err)
	}
	n := binary.LittleEndian.Uint64(size[:])
	if n < 3 || n > 64 {
		return "", fmt.Errorf("hello answered %d bytes, not a version", n)
	}
	rsp := make([]byte, n)
	if _, err := io.ReadFull(conn, rsp); err != nil {
		return "", fmt.Errorf("hello answer cut short: %w", err)
	}
	return fmt.Sprintf("%d.%d.%d", rsp[0], rsp[1], rsp[2]), nil
}
