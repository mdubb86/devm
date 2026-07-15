package serviceapi

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/mdubb86/devm/internal/debuglog"
)

// ntpUnixEpochOffset converts between UNIX (1970-01-01) and NTP (1900-01-01)
// seconds. 70 years * 365 days + 17 leap days.
const ntpUnixEpochOffset = 2208988800

// sntpPacketLen is the fixed length of an SNTPv4 header. Requests and
// responses are always exactly 48 bytes — anything else is malformed.
const sntpPacketLen = 48

// NTPServer answers SNTPv4 requests using the host's wall clock as the
// authoritative source. The daemon runs this once, eagerly on startup —
// there's exactly one Mac clock, so there's exactly one responder. Each
// project's guest DNATs its outbound UDP:123 traffic to this port so the
// guest's systemd-timesyncd talks to us without egressing to the public
// internet.
//
// We report stratum 1 (primary reference) with reference ID "MACH". That's
// technically fibbing — we're not a hardware clock, we're the Mac —
// but timesyncd doesn't care about strata beyond "is it > 0 and < 16",
// and marking ourselves as a plausible primary skips its "prefer higher-
// strata upstreams" logic. Nothing else on the guest talks to us.
type NTPServer struct {
	conn *net.UDPConn
	port atomic.Int32
}

// NewNTPServer prepares an SNTP responder bound to an ephemeral UDP port.
// The port is picked eagerly (bind on 0.0.0.0:0) so Port() is stable to
// read before Serve is called — /vm/start needs the port at nftables-
// script build time, which can race Serve on daemon startup.
func NewNTPServer() (*NTPServer, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("bind sntp udp: %w", err)
	}
	s := &NTPServer{conn: conn}
	s.port.Store(int32(conn.LocalAddr().(*net.UDPAddr).Port))
	return s, nil
}

// Port returns the picked UDP port. Safe to call before Serve.
func (s *NTPServer) Port() int {
	return int(s.port.Load())
}

// Serve blocks reading requests and sending responses until ctx is
// cancelled. Malformed packets (wrong length, unparseable) are silently
// dropped — SNTP has no error channel, and logging every stray packet
// on a well-known port would just spam.
func (s *NTPServer) Serve(ctx context.Context) error {
	debuglog.Logf("ntp", "listening on udp %s", s.conn.LocalAddr())
	go func() {
		<-ctx.Done()
		_ = s.conn.Close()
	}()
	buf := make([]byte, 64) // 48 is the max valid; give a small margin so oversized frames don't split.
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if n != sntpPacketLen {
			continue
		}
		recvTime := time.Now()
		reply := buildSNTPReply(buf[:sntpPacketLen], recvTime, time.Now())
		if _, err := s.conn.WriteToUDP(reply, addr); err != nil {
			debuglog.Logf("ntp", "write reply to %s: %v", addr, err)
		}
	}
}

// buildSNTPReply constructs a 48-byte SNTPv4 server response. The
// client's Transmit Timestamp (bytes 24..31 of req) is echoed back as
// our Origin Timestamp — that's how the client matches replies to its
// outstanding requests. recvTime + txTime are host-side timestamps; a
// well-behaved caller passes txTime as `time.Now()` right before the
// write so the response Transmit is as close to on-wire as possible.
func buildSNTPReply(req []byte, recvTime, txTime time.Time) []byte {
	out := make([]byte, sntpPacketLen)
	// Byte 0: LI=0 (no warning), VN=4, Mode=4 (server). Echo the client's
	// VN if present so v3 clients get a v3 response — some legacy stacks
	// reject a v4 response to a v3 query.
	vn := (req[0] >> 3) & 0x07
	if vn == 0 {
		vn = 4
	}
	out[0] = (0 << 6) | (vn << 3) | 4
	out[1] = 1    // Stratum: primary
	out[2] = 4    // Poll interval (2^4 = 16s min); advisory only.
	out[3] = 0xEC // Precision: -20, ~1µs.

	// Root Delay + Root Dispersion — 0 (we are the reference).
	// Reference ID — 4 ASCII bytes identifying our clock source. "MACH"
	// so a curious operator can `chronyc sources` and see who we are.
	copy(out[12:16], []byte("MACH"))

	// Reference Timestamp — last time we "synced". We're never out of
	// sync (we ARE the clock), so use now.
	writeNTPTimestamp(out[16:24], txTime)
	// Origin Timestamp — echoed from client's Transmit field.
	copy(out[24:32], req[40:48])
	// Receive Timestamp — when we saw the request.
	writeNTPTimestamp(out[32:40], recvTime)
	// Transmit Timestamp — when we sent the reply. Refresh right before
	// return so the on-wire value reflects actual send time.
	writeNTPTimestamp(out[40:48], txTime)
	return out
}

// writeNTPTimestamp encodes t as an 8-byte NTP timestamp: 32 bits of
// seconds since 1900, 32 bits of fractional second (fraction * 2^32).
func writeNTPTimestamp(dst []byte, t time.Time) {
	secs := uint32(t.Unix() + ntpUnixEpochOffset)
	// Nanos / 1e9 * 2^32. Order matters — do the multiply first to keep
	// precision inside a uint64.
	frac := uint32((uint64(t.Nanosecond()) << 32) / 1_000_000_000)
	binary.BigEndian.PutUint32(dst[0:4], secs)
	binary.BigEndian.PutUint32(dst[4:8], frac)
}
