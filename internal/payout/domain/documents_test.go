package domain

import "testing"

func TestLateDocumentsDoNotChangeMoney(t *testing.T) {
	required := true
	d := PayoutDocuments{ManualRequired: &required, ManualStatus: "new"}
	if d.Ready("selfemployed") {
		t.Fatal("manual receipt is still pending")
	}
	d, err := d.Merge(PayoutDocuments{FNS: []ReceiptDocument{{Type: "npd", URL: "https://receipt.example.test/1"}}, ManualRequired: &required, ManualStatus: "applied"})
	if err != nil || !d.Ready("selfemployed") {
		t.Fatal(err)
	}
	if _, err := d.Merge(PayoutDocuments{ReceiptURL: "javascript:alert(1)"}); err == nil {
		t.Fatal("unsafe receipt accepted")
	}
	got, err := d.Merge(PayoutDocuments{})
	if err != nil || len(got.FNS) != 1 {
		t.Fatal("empty response erased receipt")
	}
	d, err = d.Merge(PayoutDocuments{AgreementURL: "https://docs.example.test/agreement", AcceptanceURL: "https://docs.example.test/act"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = d.Merge(PayoutDocuments{})
	if err != nil || got.AgreementURL != d.AgreementURL || got.AcceptanceURL != d.AcceptanceURL {
		t.Fatal("document links lost")
	}
	if _, err := d.Merge(PayoutDocuments{AgreementSigningURL: "http://unsafe.example.test"}); err == nil {
		t.Fatal("unsafe signing link accepted")
	}
}
