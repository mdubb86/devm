package serviceapi

import (
	"encoding/binary"
	"errors"
	"net"
)

// parseSNI reads a TLS record from the start of record, parses the
// ClientHello handshake message, and returns the server_name
// extension's host_name value. Returns "" with nil error if the
// ClientHello carried no SNI extension, or if its server_name list
// contained only non-hostname entries, or if a host_name entry was
// empty — callers treat empty-SNI as "unknown-SNI" fail-closed, same
// as a mismatched one. Returned names are not normalised; callers
// handle case and any trailing dot.
//
// Errors are structural: not a TLS record, truncated, non-ClientHello
// handshake. Rolled by hand (no new dependency); see RFC 5246 §7.4.1.2
// and RFC 6066 §3 for the wire format.
func parseSNI(record []byte) (string, error) {
	if len(record) < 5 {
		return "", errors.New("sni: truncated record header")
	}
	if record[0] != 0x16 {
		return "", errors.New("sni: not a TLS handshake record")
	}
	recordLen := int(binary.BigEndian.Uint16(record[3:5]))
	if len(record) < 5+recordLen {
		return "", errors.New("sni: truncated record body")
	}
	body := record[5 : 5+recordLen]
	if len(body) < 4 || body[0] != 0x01 {
		return "", errors.New("sni: not a ClientHello")
	}
	// handshake len (24-bit) at body[1:4]; body[4:] is ClientHello
	helloLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if len(body) < 4+helloLen {
		return "", errors.New("sni: truncated handshake")
	}
	h := body[4 : 4+helloLen]
	// h: version(2) random(32) sessionID(1+n) ciphers(2+n) compression(1+n) extensions(2+n)
	if len(h) < 2+32+1 {
		return "", errors.New("sni: truncated hello")
	}
	off := 2 + 32
	sidLen := int(h[off])
	off++
	off += sidLen
	if len(h) < off+2 {
		return "", errors.New("sni: truncated ciphers")
	}
	cipherLen := int(binary.BigEndian.Uint16(h[off : off+2]))
	off += 2 + cipherLen
	if len(h) < off+1 {
		return "", errors.New("sni: truncated compression")
	}
	compLen := int(h[off])
	off += 1 + compLen
	if len(h) < off {
		return "", errors.New("sni: truncated compression")
	}
	if len(h) == off {
		return "", nil // valid ClientHello with no extensions block
	}
	if len(h) < off+2 {
		return "", errors.New("sni: truncated extensions header")
	}
	extTotal := int(binary.BigEndian.Uint16(h[off : off+2]))
	off += 2
	if len(h) < off+extTotal {
		return "", errors.New("sni: truncated extensions")
	}
	exts := h[off : off+extTotal]
	for len(exts) > 0 {
		if len(exts) < 4 {
			return "", errors.New("sni: truncated extension header")
		}
		extType := binary.BigEndian.Uint16(exts[0:2])
		extLen := int(binary.BigEndian.Uint16(exts[2:4]))
		if len(exts) < 4+extLen {
			return "", errors.New("sni: truncated extension")
		}
		extBody := exts[4 : 4+extLen]
		exts = exts[4+extLen:]
		if extType != 0x0000 { // server_name
			continue
		}
		// extBody: list_len(2) [ name_type(1) name_len(2) name(n) ]*
		if len(extBody) < 2 {
			return "", errors.New("sni: bad server_name")
		}
		listLen := int(binary.BigEndian.Uint16(extBody[0:2]))
		if len(extBody) < 2+listLen {
			return "", errors.New("sni: bad server_name list")
		}
		list := extBody[2 : 2+listLen]
		for len(list) >= 3 {
			nameType := list[0]
			nameLen := int(binary.BigEndian.Uint16(list[1:3]))
			if len(list) < 3+nameLen {
				return "", errors.New("sni: bad server_name entry")
			}
			if nameType == 0x00 {
				return string(list[3 : 3+nameLen]), nil
			}
			list = list[3+nameLen:]
		}
		return "", nil
	}
	return "", nil
}

// prefixConn wraps net.Conn with a bytes slice already read from the
// stream, so a caller that peeked bytes (via a bufio.Reader) can hand
// the connection to a consumer (e.g. tls.Server) that will read from
// the start. Read returns the prefix first (short reads leave the
// remainder for the next call), then falls through to the underlying
// Conn; once the prefix is drained an empty-prefix Read is a direct
// passthrough. Prefix bytes are served even after the underlying Conn
// is closed, until exhausted. Write, Close, deadlines, and addrs pass
// through.
type prefixConn struct {
	prefix []byte
	net.Conn
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
