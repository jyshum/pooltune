package burst

import (
	"encoding/binary"
	"net"
	"strings"
)

// A private resolver for a single explicit IP/name mapping. TLS still verifies
// the original hostname. It does not forward requests or change system DNS.
func startPinnedDNS(name, address string) (net.PacketConn, error) {
	ip := net.ParseIP(address)
	if ip == nil || !atom.MatchString(name) {
		return nil, ErrUnsafe
	}
	pc, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, peer, e := pc.ReadFrom(buf)
			if e != nil {
				return
			}
			if n < 17 || binary.BigEndian.Uint16(buf[4:6]) != 1 {
				continue
			}
			q := append([]byte(nil), buf[:n]...)
			pos := 12
			labels := []string{}
			valid := true
			for {
				if pos >= n {
					valid = false
					break
				}
				size := int(q[pos])
				pos++
				if size == 0 {
					break
				}
				if size > 63 || pos+size > n {
					valid = false
					break
				}
				labels = append(labels, string(q[pos:pos+size]))
				pos += size
			}
			if !valid || pos+4 > n {
				continue
			}
			q = q[:pos+4]
			typ := binary.BigEndian.Uint16(q[pos : pos+2])
			class := binary.BigEndian.Uint16(q[pos+2:])
			q[2] = 0x81
			q[3] = 0x80
			for i := 6; i < 12; i++ {
				q[i] = 0
			}
			match := strings.EqualFold(strings.Join(labels, "."), strings.TrimSuffix(name, "."))
			if !match {
				q[3] = 0x83
			} else if class == 1 {
				var data net.IP
				if typ == 1 {
					data = ip.To4()
				}
				if typ == 28 && ip.To4() == nil {
					data = ip.To16()
				}
				if data != nil {
					q[7] = 1
					q = append(q, 0xc0, 0x0c, byte(typ>>8), byte(typ), 0, 1, 0, 0, 0, 30, 0, byte(len(data)))
					q = append(q, data...)
				}
			}
			_, _ = pc.WriteTo(q, peer)
		}
	}()
	return pc, nil
}
