package domain

import "testing"

func TestDevPaymentMarker(t *testing.T) {
	for _, tc := range []struct {
		purpose string
		valid   bool
	}{
		{"Оплата MX-123. DEV-TBANK-DEV-SERVER.", true},
		{"dev-tbank-dev-server", true},
		{"DEV-TBANK-DEV-SERVER-OTHER", false},
		{"XDEV-TBANK-DEV-SERVER", false},
		{"MX-123", false},
	} {
		if HasDevPaymentMarker(tc.purpose, "dev-server") != tc.valid {
			t.Errorf("marker %q", tc.purpose)
		}
	}
	for _, code := range []string{"", "a b", "абв", "ab", "x\ny"} {
		if ValidDevPaymentCode(code) {
			t.Errorf("invalid code %q", code)
		}
	}
}
