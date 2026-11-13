package serviceapi

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestAgentFilterVerdictFor_Table(t *testing.T) {
	forward := []byte{11, 13} // REQUEST_IDENTITIES, SIGN_REQUEST
	reject := []byte{
		17, 25, // ADD_IDENTITY, ADD_ID_CONSTRAINED
		18, 19, // REMOVE_IDENTITY, REMOVE_ALL_IDENTITIES
		20, 26, // ADD_SMARTCARD_KEY, ADD_SMARTCARD_KEY_CONSTRAINED
		21,     // REMOVE_SMARTCARD_KEY
		22, 23, // LOCK, UNLOCK
		27,                     // EXTENSION
		0, 1, 99, 100, 200, 255, // unknowns
	}
	for _, b := range forward {
		if AgentFilterVerdictFor(b) != AgentForward {
			t.Errorf("type %d: want AgentForward", b)
		}
	}
	for _, b := range reject {
		if AgentFilterVerdictFor(b) != AgentReject {
			t.Errorf("type %d: want AgentReject", b)
		}
	}
}

func TestReadWriteAgentMessage_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAgentMessage(&buf, 13, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadAgentMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != 13 {
		t.Fatalf("type %d want 13", typ)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload %q want %q", payload, "hello")
	}
}

func TestReadAgentMessage_ZeroPayload(t *testing.T) {
	// length=1 means type byte only, zero-byte payload.
	raw := []byte{0, 0, 0, 1, AgentRequestIdentities}
	typ, payload, err := ReadAgentMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if typ != AgentRequestIdentities {
		t.Fatalf("type %d", typ)
	}
	if len(payload) != 0 {
		t.Fatalf("payload len %d", len(payload))
	}
}

func TestReadAgentMessage_RejectsZeroLength(t *testing.T) {
	// length=0 would mean no type byte — malformed.
	raw := []byte{0, 0, 0, 0}
	_, _, err := ReadAgentMessage(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("want error on zero-length frame")
	}
}

func TestReadAgentMessage_RejectsOversizedLength(t *testing.T) {
	// length=262145 (one past AGENT_MAX_LEN) must be rejected at the
	// length check before any body allocation. We deliberately write
	// only 5 bytes (header + type) — if the reader allocated first it
	// would block or EOF instead of returning our length error.
	raw := []byte{0x00, 0x04, 0x00, 0x01, AgentSignRequest}
	_, _, err := ReadAgentMessage(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("want error on oversized length")
	}
	if !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("want error containing %q, got %v", "exceeds max", err)
	}
}

func TestReadAgentMessage_TruncatedIsErr(t *testing.T) {
	raw := []byte{0, 0, 0, 10, AgentSignRequest, 'x', 'y'} // claims 10, only 3 bytes
	_, _, err := ReadAgentMessage(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("want error on truncated frame")
	}
	if err == io.EOF {
		// io.ReadFull turns a short read into ErrUnexpectedEOF; EOF would be a
		// bug (losing which error it was). Pin the unexpected-EOF path.
		t.Fatal("want ErrUnexpectedEOF, got EOF")
	}
}
