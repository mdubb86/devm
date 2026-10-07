package serviceapi

import (
	"bufio"
	"bytes"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
	require.Contains(t, err.Error(), "malformed",
		"err must identify the shape rejection: %v", err)
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
	require.Error(t, err, "expected deadline error")
	require.GreaterOrEqual(t, elapsed, 400*time.Millisecond,
		"probe must wait near 500ms, returned at %s", elapsed)
	require.Less(t, elapsed, 1*time.Second,
		"probe took too long: %s", elapsed)

	var netErr net.Error
	if errors.As(err, &netErr) {
		require.True(t, netErr.Timeout(), "err must be timeout, got %v", err)
	} else {
		require.ErrorIs(t, err, os.ErrDeadlineExceeded,
			"err must be deadline-exceeded, got %v", err)
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

func TestNewSoftnetDriftInfo_ProbeErrorWithMatchingSHAIsDrift(t *testing.T) {
	// A probe error means we did not learn the sha from softnet, so a
	// matching value is coincidence.
	info := newSoftnetDriftInfo("sewtrue", "/workspace/sewtrue",
		softnet.ContractSHA, softnet.ContractSHA, errFake{})
	require.NotNil(t, info, "probe error must be surfaced as drift even when SHAs happen to match")
}

func TestFormatDriftMessage_TruncationAndFallbacks(t *testing.T) {
	cases := []struct {
		name, projectName, projectDir, localSHA, remoteSHA string
		wants                                              []string
	}{
		{"happy path truncation", "sewtrue", "/workspace/sewtrue",
			"aaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb", "ffffffffffffffffcccccccccccccccc",
			[]string{"sewtrue", "aaaaaaaaaaaa", "ffffffffffff", "/workspace/sewtrue", "devm stop && devm start"}},
		{"empty projectDir", "sewtrue", "", "aaaaaaaaaaaa", "ffffffffffff",
			[]string{"<your project directory>"}},
		{"empty remoteSHA probe-error", "sewtrue", "/workspace/sewtrue", "aaaaaaaaaaaabbbbbbbbbbbb", "",
			[]string{"unreachable"}},
		{"short SHA stays whole", "sewtrue", "/d", "abc", "def",
			[]string{"abc", "def"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := formatDriftMessage(c.projectName, c.projectDir, c.localSHA, c.remoteSHA)
			for _, want := range c.wants {
				require.Contains(t, msg, want, "message missing %q: %q", want, msg)
			}
		})
	}
	// Truncation must actually cut.
	msg := formatDriftMessage("p", "/d", "aaaaaaaaaaaaaaaabbbb", "ffffffffffffffffcccc")
	require.NotContains(t, msg, "aaaaaaaaaaaaa")
	require.NotContains(t, msg, "fffffffffffff")
}

type errFake struct{}

func (errFake) Error() string { return "fake probe error" }

func TestLogDriftIfAny_SilentWhenInSync(t *testing.T) {
	sha := softnet.ContractSHA
	sock := fakeSoftnetSock(t, func(_ []byte) []byte {
		return []byte(`{"ok":true,"sha":"` + sha + `"}` + "\n")
	})
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	logDriftIfAny("sewtrue", "/workspace/sewtrue", sock)

	if strings.Contains(buf.String(), "softnet-drift") {
		t.Fatalf("in-sync VM must not log drift, got %q", buf.String())
	}
}

func TestLogDriftIfAny_WritesOneLineWhenDrifted(t *testing.T) {
	remote := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	sock := fakeSoftnetSock(t, func(_ []byte) []byte {
		return []byte(`{"ok":true,"sha":"` + remote + `"}` + "\n")
	})
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	logDriftIfAny("sewtrue", "/workspace/sewtrue", sock)

	out := buf.String()
	if !strings.Contains(out, "softnet-drift") || !strings.Contains(out, "sewtrue") {
		t.Fatalf("drift log missing project name / tag: %q", out)
	}
	if strings.Count(out, "softnet-drift") != 1 || strings.Count(out, "\n") != 1 {
		t.Fatalf("drift log must be exactly one line, got %q", out)
	}
}
