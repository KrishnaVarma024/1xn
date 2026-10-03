package gst

import (
	"testing"
	"time"
)

func TestFY(t *testing.T) {
	cases := []struct {
		at   string
		want string
	}{
		{"2026-10-05T21:14:03+05:30", "2627"},
		{"2027-03-31T23:59:59+05:30", "2627"},
		{"2027-04-01T00:30:00+05:30", "2728"},
		// 31 Mar 19:00 UTC is already 1 Apr 00:30 in India: the FY follows IST.
		{"2027-03-31T19:00:00Z", "2728"},
		{"2099-12-31T12:00:00+05:30", "9900"},
	}
	for _, c := range cases {
		at, err := time.Parse(time.RFC3339, c.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := FY(at); got != c.want {
			t.Errorf("FY(%s) = %s, want %s", c.at, got, c.want)
		}
	}
}

func TestInvoiceNo(t *testing.T) {
	s := InvoiceNo("00A", "2627", 42)
	if s != "00A-2627-000042" || len(s) > 16 {
		t.Fatalf("got %q", s)
	}
	p, fy, seq, err := ParseInvoiceNo(s)
	if err != nil || p != "00A" || fy != "2627" || seq != 42 {
		t.Fatalf("parse: %v %v %v %v", p, fy, seq, err)
	}
	for _, bad := range []string{"", "00A-2627-000000", "00a-2627-000001", "00A/2627/000001", "00A-2627-0000001"} {
		if _, _, _, err := ParseInvoiceNo(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestSeriesPrefix(t *testing.T) {
	for n, want := range map[int]string{1: "001", 35: "00Z", 36: "010", 46655: "ZZZ"} {
		got, err := SeriesPrefix(n)
		if err != nil || got != want {
			t.Errorf("SeriesPrefix(%d) = %q, %v; want %q", n, got, err, want)
		}
	}
	if _, err := SeriesPrefix(46656); err == nil {
		t.Error("46656 should overflow 3 characters")
	}
}

func TestValidGSTIN(t *testing.T) {
	// 27AAPFU0939F1ZV is the sample GSTIN used in GST portal documentation.
	if !ValidGSTIN("27AAPFU0939F1ZV") {
		t.Fatal("sample GSTIN should be valid")
	}
	for _, bad := range []string{
		"27AAPFU0939F1ZW", // wrong check digit
		"27AAPFU0939F1Z",  // too short
		"27aapfu0939f1zv", // lower case
		"27AAPFU0939F0ZV", // entity number cannot be 0
	} {
		if ValidGSTIN(bad) {
			t.Errorf("%s should be invalid", bad)
		}
	}
}

func TestValidStateCode(t *testing.T) {
	for _, c := range []string{"01", "07", "26", "27", "29", "36", "37", "38", "97"} {
		if !ValidStateCode(c) {
			t.Errorf("%s should be a state in use", c)
		}
	}
	// 25 merged into 26, 28 is pre-2014 Andhra Pradesh, 99 is the Centre's own
	// jurisdiction: none can be a restaurant's state. Nor can non-codes.
	for _, c := range []string{"", "00", "25", "28", "39", "96", "99", "9", "290", "2a"} {
		if ValidStateCode(c) {
			t.Errorf("%q should be refused", c)
		}
	}
}
