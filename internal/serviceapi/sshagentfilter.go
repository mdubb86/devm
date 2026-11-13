package serviceapi

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Agent protocol message type bytes (draft-miller-ssh-agent). Named
// here so the filter table and tests reference the same constants.
const (
	AgentFailure           byte = 5
	AgentRequestIdentities byte = 11
	AgentSignRequest       byte = 13
)

// agentMaxMessageLen is OpenSSH's AGENT_MAX_LEN. A compromised guest
// can otherwise send any uint32 and force an unbounded allocation per
// stream before any read of the body — enough to OOM the daemon in
// parallel.
const agentMaxMessageLen = 262144

// AgentFilterVerdict is what the Mac-side bridge does with a message
// read from the guest.
type AgentFilterVerdict int

const (
	// AgentForward passes the message to Mac's ssh-agent unchanged.
	AgentForward AgentFilterVerdict = iota
	// AgentReject answers the guest with AgentFailure and does not
	// touch Mac's agent. Covers key-planting (ADD_IDENTITY),
	// key-removal (REMOVE_*), agent lock (LOCK/UNLOCK), smartcard
	// management, extensions, and every unknown type. Default-deny.
	AgentReject
)

// AgentFilterVerdictFor returns the verdict for one message type byte.
// Only REQUEST_IDENTITIES (11) and SIGN_REQUEST (13) forward; everything
// else is rejected — see the design doc's Review Focus for the threat
// model.
func AgentFilterVerdictFor(t byte) AgentFilterVerdict {
	switch t {
	case AgentRequestIdentities, AgentSignRequest:
		return AgentForward
	default:
		return AgentReject
	}
}

// ReadAgentMessage reads one `uint32 length | byte type | payload` frame.
// length includes the type byte. length=0 is rejected (malformed).
// Returns io.ErrUnexpectedEOF on truncated frames.
func ReadAgentMessage(r io.Reader) (byte, []byte, error) {
	var ln uint32
	if err := binary.Read(r, binary.BigEndian, &ln); err != nil {
		return 0, nil, err
	}
	if ln == 0 {
		return 0, nil, fmt.Errorf("agent frame: zero length")
	}
	if ln > agentMaxMessageLen {
		return 0, nil, fmt.Errorf("agent frame: length %d exceeds max %d", ln, agentMaxMessageLen)
	}
	buf := make([]byte, ln)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

// WriteAgentMessage writes one framed message to w.
func WriteAgentMessage(w io.Writer, msgType byte, payload []byte) error {
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(payload)+1))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if _, err := w.Write([]byte{msgType}); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}
