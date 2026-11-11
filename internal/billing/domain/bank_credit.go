package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	BankReadOnly   = "readonly"
	BankControlled = "controlled"
	BankAutomation = "automation"
)

var ErrBankCreditRestricted = errors.New("bank credit restricted")

func DenyBankCredit(reason string) error {
	return fmt.Errorf("%w: %s", ErrBankCreditRestricted, reason)
}

var devPaymentCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,48}$`)

func ValidDevPaymentCode(code string) bool { return devPaymentCodePattern.MatchString(code) }
func DevPaymentMarker(code string) string  { return "DEV-TBANK-" + strings.ToUpper(code) }

// HasDevPaymentMarker requires a whole token; a shared prefix is not sufficient.
func HasDevPaymentMarker(purpose, code string) bool {
	if !ValidDevPaymentCode(code) {
		return false
	}
	marker := DevPaymentMarker(code)
	for _, token := range strings.FieldsFunc(strings.ToUpper(purpose), func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		if token == marker {
			return true
		}
	}
	return false
}
