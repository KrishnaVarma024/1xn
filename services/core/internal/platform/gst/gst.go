// Package gst holds the Indian GST rules that are not arithmetic: financial
// years, invoice numbers and GSTIN validation. The arithmetic lives in
// modules/sales/pricing.
package gst

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// IST is India Standard Time. India has no daylight saving, so a fixed
// +05:30 offset is exact and needs no timezone database on the server.
var IST = time.FixedZone("IST", 5*3600+30*60)

// FY returns the Indian financial year (April to March) that t falls in, as
// four digits: 5 Oct 2026 -> "2627", 31 Mar 2027 -> "2627", 1 Apr 2027 -> "2728".
//
// It uses the moment of issue in IST, not the restaurant's business date: a
// bill printed at 00:30 on 1 April belongs to the NEW financial year for GST,
// even though the kitchen still counts it as the 31 March night shift.
func FY(t time.Time) string {
	t = t.In(IST)
	start := t.Year()
	if t.Month() < time.April {
		start--
	}
	return fmt.Sprintf("%02d%02d", start%100, (start+1)%100)
}

// invoiceRe is PPP-FFFF-NNNNNN: device series prefix, financial year, running
// number. 15 characters, inside rule 46(b)'s limit of 16, and it only uses
// letters, digits and "-" as that rule allows.
var invoiceRe = regexp.MustCompile(`^([0-9A-Z]{3})-([0-9]{4})-([0-9]{6})$`)

// InvoiceNo formats an invoice number.
func InvoiceNo(prefix, fy string, seq int) string {
	return fmt.Sprintf("%s-%s-%06d", prefix, fy, seq)
}

// ParseInvoiceNo splits an invoice number into its parts.
func ParseInvoiceNo(s string) (prefix, fy string, seq int, err error) {
	m := invoiceRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", 0, fmt.Errorf("invoice number %q is not PPP-FFFF-NNNNNN", s)
	}
	seq, _ = strconv.Atoi(m[3])
	if seq < 1 {
		return "", "", 0, fmt.Errorf("invoice number %q: running number starts at 000001", s)
	}
	return m[1], m[2], seq, nil
}

// SeriesPrefix turns the n-th device of a legal entity into its 3-character
// series prefix in base 36: 1 -> "001", 36 -> "010", 46655 -> "ZZZ".
func SeriesPrefix(n int) (string, error) {
	if n < 1 || n > 36*36*36-1 {
		return "", fmt.Errorf("series number %d out of range", n)
	}
	s := strings.ToUpper(strconv.FormatInt(int64(n), 36))
	return strings.Repeat("0", 3-len(s)) + s, nil
}

const gstinAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

var gstinRe = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`)

// ValidGSTIN checks a GSTIN's shape and its check digit. The 15th character
// is a mod-36 checksum over the first 14; one mistyped character fails it, so
// a wrong GSTIN is caught at setup instead of on a customer's tax invoice.
func ValidGSTIN(g string) bool {
	if !gstinRe.MatchString(g) {
		return false
	}
	sum := 0
	for i := 0; i < 14; i++ {
		v := strings.IndexByte(gstinAlphabet, g[i])
		factor := 1
		if i%2 == 1 {
			factor = 2
		}
		p := v * factor
		sum += p/36 + p%36
	}
	check := (36 - sum%36) % 36
	return gstinAlphabet[check] == g[14]
}

// stateCodes are the GST state codes a business can register under today:
// 01 to 38 and 97 (Other Territory), less 25 (Daman and Diu, merged into 26
// in 2020) and 28 (Andhra Pradesh before 2014; GST began with 37).
var stateCodes = func() map[string]bool {
	m := map[string]bool{"97": true}
	for i := 1; i <= 38; i++ {
		if i != 25 && i != 28 {
			m[fmt.Sprintf("%02d", i)] = true
		}
	}
	return m
}()

// ValidStateCode reports whether code is a GST state code in use. The state
// decides every bill's tax: the same state means CGST and SGST, another state
// IGST. So a mistyped one is refused, never stored.
func ValidStateCode(code string) bool { return stateCodes[code] }
