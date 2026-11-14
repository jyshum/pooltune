package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	hostnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"net"
	"os"
	"path/filepath"
)

// Match the server end of this exact TCP connection to an owned local process.
// A TLS-valid loopback tunnel is insufficient evidence of database-host pressure.
func localPeer(ctx context.Context, c *pgx.Conn, executable string, expectedPID int32) (string, error) {
	client, ok := c.PgConn().Conn().LocalAddr().(*net.TCPAddr)
	if !ok {
		return "", ErrUnsupported
	}
	server, ok := c.PgConn().Conn().RemoteAddr().(*net.TCPAddr)
	if !ok {
		return "", ErrUnsupported
	}
	connections, e := hostnet.ConnectionsPidWithContext(ctx, "tcp", expectedPID)
	if e != nil {
		return "", ErrUnsupported
	}
	for _, socket := range connections {
		if socket.Pid <= 0 || (expectedPID != 0 && socket.Pid != expectedPID) || socket.Status != "ESTABLISHED" || socket.Laddr.Port != uint32(server.Port) || socket.Raddr.Port != uint32(client.Port) || !net.ParseIP(socket.Laddr.IP).Equal(server.IP) || !net.ParseIP(socket.Raddr.IP).Equal(client.IP) {
			continue
		}
		p, e := process.NewProcessWithContext(ctx, socket.Pid)
		if e != nil {
			return "", ErrUnsupported
		}
		path, e := p.ExeWithContext(ctx)
		if e != nil || filepath.Base(path) != executable {
			return "", ErrUnsupported
		}
		uids, e := p.UidsWithContext(ctx)
		if e != nil || len(uids) == 0 {
			return "", ErrUnsupported
		}
		for _, uid := range uids {
			if uid != uint32(os.Getuid()) {
				return "", ErrUnsupported
			}
		}
		started, e := p.CreateTimeWithContext(ctx)
		if e != nil || started <= 0 {
			return "", ErrUnsupported
		}
		return fmt.Sprintf("%d/%d/%s", socket.Pid, started, path), nil
	}
	return "", ErrUnsupported
}

func localResourceIdentity(ctx context.Context, admin, monitor *pgx.Conn) (string, error) {
	pooler, e := localPeer(ctx, admin, "pgbouncer", 0)
	if e != nil {
		return "", e
	}
	var cluster, database, role string
	var pid int32
	if monitor.QueryRow(ctx, "SELECT system_identifier::text, (SELECT oid::text FROM pg_database WHERE datname=current_database()), (SELECT oid::text FROM pg_roles WHERE rolname=current_user), pg_backend_pid() FROM pg_control_system()").Scan(&cluster, &database, &role, &pid) != nil || cluster == "" || database == "" || role == "" {
		return "", ErrUnsupported
	}
	if _, e := localPeer(ctx, monitor, "postgres", pid); e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(cluster + "/" + database + "/" + role + "/" + pooler))
	return hex.EncodeToString(sum[:]), nil
}
