package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrintVersion_PlainText(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf, "v0.1.0", "abc1234", "2026-06-07", "fp-test-abcd", "", false)
	got := buf.String()

	if !strings.Contains(got, "devm v0.1.0") {
		t.Errorf("expected 'devm v0.1.0' in output, got: %q", got)
	}
	if !strings.Contains(got, "abc1234") {
		t.Errorf("expected commit 'abc1234' in output, got: %q", got)
	}
	if !strings.Contains(got, "2026-06-07") {
		t.Errorf("expected date '2026-06-07' in output, got: %q", got)
	}
}

func TestPrintVersion_WithNewerAvailable(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf, "v0.1.0", "abc1234", "2026-06-07", "fp-test-abcd", "v0.2.0", false)
	got := buf.String()

	if !strings.Contains(got, "newer version v0.2.0 available") {
		t.Errorf("expected upgrade hint in output, got: %q", got)
	}
	if !strings.Contains(got, "devm upgrade") {
		t.Errorf("expected 'devm upgrade' in output, got: %q", got)
	}
}

func TestPrintVersion_JSON(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf, "v0.1.0", "abc1234", "2026-06-07", "fp-test-abcd", "v0.2.0", true)

	var out map[string]string
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON decode failed: %v\noutput: %q", err, buf.String())
	}

	if out["version"] != "v0.1.0" {
		t.Errorf("version = %q, want %q", out["version"], "v0.1.0")
	}
	if out["commit"] != "abc1234" {
		t.Errorf("commit = %q, want %q", out["commit"], "abc1234")
	}
	if out["date"] != "2026-06-07" {
		t.Errorf("date = %q, want %q", out["date"], "2026-06-07")
	}
	if out["latest"] != "v0.2.0" {
		t.Errorf("latest = %q, want %q", out["latest"], "v0.2.0")
	}
	if out["upgrade_command"] != "devm upgrade" {
		t.Errorf("upgrade_command = %q, want %q", out["upgrade_command"], "devm upgrade")
	}
}

func TestPrintVersion_JSON_NoLatestOmitsField(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf, "v0.1.0", "abc1234", "2026-06-07", "fp-test-abcd", "", true)

	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON decode failed: %v\noutput: %q", err, buf.String())
	}

	if _, ok := out["latest"]; ok {
		t.Errorf("expected 'latest' field to be absent when no newer version, got: %v", out)
	}
	if _, ok := out["upgrade_command"]; ok {
		t.Errorf("expected 'upgrade_command' field to be absent when no newer version, got: %v", out)
	}
}
