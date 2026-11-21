package schema

import (
	"strings"
	"testing"
)

const gib int64 = 1024 * 1024 * 1024

func TestParseSizeOrPercent_Absolute(t *testing.T) {
	cases := []string{"6G", "6GB", "6GiB", "6g"}
	want := 6 * gib
	for _, in := range cases {
		got, err := ParseSizeOrPercent(in)
		if err != nil {
			t.Fatalf("ParseSizeOrPercent(%q) returned error: %v", in, err)
		}
		if got.Absolute != want {
			t.Errorf("ParseSizeOrPercent(%q).Absolute = %d, want %d", in, got.Absolute, want)
		}
		if got.Percent != 0 {
			t.Errorf("ParseSizeOrPercent(%q).Percent = %v, want 0", in, got.Percent)
		}
	}
}

func TestParseSizeOrPercent_Percent(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"15%", 0.15},
		{"50%", 0.5},
		{"200%", 2.0},
	}
	for _, tc := range cases {
		got, err := ParseSizeOrPercent(tc.in)
		if err != nil {
			t.Fatalf("ParseSizeOrPercent(%q) returned error: %v", tc.in, err)
		}
		if got.Percent != tc.want {
			t.Errorf("ParseSizeOrPercent(%q).Percent = %v, want %v", tc.in, got.Percent, tc.want)
		}
		if got.Absolute != 0 {
			t.Errorf("ParseSizeOrPercent(%q).Absolute = %d, want 0", tc.in, got.Absolute)
		}
	}
}

func TestParseSizeOrPercent_Rejects(t *testing.T) {
	cases := []string{"0", "-1G", "0%", "abc", "", "6", "%", "NaN%", "Inf%", "-Inf%"}
	for _, in := range cases {
		_, err := ParseSizeOrPercent(in)
		if err == nil {
			t.Errorf("ParseSizeOrPercent(%q) = nil error, want error", in)
			continue
		}
		// The error message must name the offending input so the
		// caller can tell which field is wrong when several parse
		// errors surface together.
		if !strings.Contains(err.Error(), in) && in != "" {
			t.Errorf("ParseSizeOrPercent(%q) error %q does not contain input", in, err.Error())
		}
		if in == "" && !strings.Contains(err.Error(), "empty") {
			t.Errorf("ParseSizeOrPercent(%q) error %q does not describe empty input", in, err.Error())
		}
	}
}

func TestSizeOrPercentResolve_Absolute(t *testing.T) {
	s := SizeOrPercent{Absolute: 6 * gib}
	got := s.Resolve(32 * gib)
	if got != 6*gib {
		t.Errorf("Resolve = %d, want %d", got, 6*gib)
	}
}

func TestSizeOrPercentResolve_Percent(t *testing.T) {
	s := SizeOrPercent{Percent: 0.15}
	base := 32 * gib
	got := s.Resolve(base)
	want := int64(float64(base) * 0.15) // truncates toward zero → floor for positive
	if got != want {
		t.Errorf("Resolve = %d, want %d", got, want)
	}
	// Sanity: result is a whole number of bytes less than the base.
	if got >= base {
		t.Errorf("Resolve = %d, want < base %d", got, base)
	}
}

func TestSizeOrPercentResolve_OverOneHundred(t *testing.T) {
	s := SizeOrPercent{Percent: 2.0}
	got := s.Resolve(8 * gib)
	if got != 16*gib {
		t.Errorf("Resolve = %d, want %d", got, 16*gib)
	}
}
