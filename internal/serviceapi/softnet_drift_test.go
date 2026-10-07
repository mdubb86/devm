package serviceapi

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdubb86/devm/internal/softnet"
)

func fakeSoftnetSock(t *testing.T, respond func(req []byte) []byte) string {
	t.Helper()
	// /tmp, not t.TempDir(): macOS caps sun_path at 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "sd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "softnet.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				resp := respond(line)
				if resp != nil {
					_, _ = c.Write(resp)
				}
			}(c)
		}
	}()
	return sock
}

func TestProbeSoftnetContract_ReturnsValidSHA(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sock := fakeSoftnetSock(t, func(_ []byte) []byte {
		return []byte(`{"ok":true,"sha":"` + sha + `"}` + "\n")
	})

	got, err := probeSoftnetContract(sock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got != sha {
		t.Fatalf("got %q want %q", got, sha)
	}
}

func TestProbeSoftnetContract_RejectsMalformedSHA(t *testing.T) {
	// Softnet responds with a NON-hex / wrong-length sha — the probe
	// must treat it as drift, not pass through unverified bytes.
	sock := fakeSoftnetSock(t, func(_ []byte) []byte {
		return []byte(`{"ok":true,"sha":"NOT_HEX"}` + "\n")
	})

	_, err := probeSoftnetContract(sock)
	if err == nil {
		t.Fatal("expected error for malformed sha")
	}
	if !strings.Contains(err.Error(), "malformed") && !strings.Contains(err.Error(), "sha") {
		t.Fatalf("err must mention the shape problem: %v", err)
	}
}

func TestProbeSoftnetContract_DeadSocketIsError(t *testing.T) {
	_, err := probeSoftnetContract("/no/such/softnet.sock")
	if err == nil {
		t.Fatal("expected error on dead socket")
	}
}

func TestProbeSoftnetContract_SilentPeerTimesOutQuickly(t *testing.T) {
	sock := fakeSoftnetSock(t, func(_ []byte) []byte {
		time.Sleep(2 * time.Second) // never responds in time
		return nil
	})
	start := time.Now()
	_, err := probeSoftnetContract(sock)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("probe should return within ~500ms, took %s", elapsed)
	}
}

func TestNewSoftnetDriftInfo_NoDriftReturnsNil(t *testing.T) {
	info := newSoftnetDriftInfo("sewtrue", "/workspace/sewtrue", softnet.ContractSHA, softnet.ContractSHA, nil)
	if info != nil {
		t.Fatalf("same sha should be no-drift, got %+v", info)
	}
}

func TestNewSoftnetDriftInfo_MismatchReturnsInfo(t *testing.T) {
	remote := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	info := newSoftnetDriftInfo("sewtrue", "/workspace/sewtrue", softnet.ContractSHA, remote, nil)
	if info == nil {
		t.Fatal("mismatch should return non-nil drift info")
	}
	if info.LocalSHA != softnet.ContractSHA || info.RemoteSHA != remote {
		t.Fatalf("drift info sha fields wrong: %+v", info)
	}
	if !strings.Contains(info.Message, "sewtrue") || !strings.Contains(info.Message, "devm stop && devm start") {
		t.Fatalf("message must name project + restart command: %q", info.Message)
	}
}

func TestNewSoftnetDriftInfo_ProbeErrorIsDrift(t *testing.T) {
	info := newSoftnetDriftInfo("sewtrue", "/workspace/sewtrue", softnet.ContractSHA, "", errFake{})
	if info == nil {
		t.Fatal("probe error must be surfaced as drift")
	}
	if info.RemoteSHA != "" {
		t.Fatalf("probe-error drift should have empty RemoteSHA, got %q", info.RemoteSHA)
	}
}

type errFake struct{}

func (errFake) Error() string { return "fake probe error" }
