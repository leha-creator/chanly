package domain

import "github.com/leha-creator/chanly/internal/platform/money"

// ApplyPostings — остатки счетов после проводок (06 §2.2 п. 2). Исходная карта не меняется.
// Пассив с отрицательным итогом — *NegativeLiabilityError с наибольшей нехваткой (п. 4);
// актив может уйти в минус.
func ApplyPostings(balances map[AccountRef]money.Kopecks, postings []Posting) (map[AccountRef]money.Kopecks, error) {
	out := make(map[AccountRef]money.Kopecks, len(balances))
	for k, v := range balances {
		out[k] = v
	}
	for _, p := range postings {
		out[p.Debit] += Delta(p.Debit.Kind.Side(), true, p.Amount)
		out[p.Credit] += Delta(p.Credit.Kind.Side(), false, p.Amount)
	}
	var worst *NegativeLiabilityError
	for _, ref := range touched(postings) {
		if ref.Kind.Side() == Liability && out[ref] < 0 {
			if worst == nil || -out[ref] > worst.Missing {
				worst = &NegativeLiabilityError{Account: ref, Missing: -out[ref]}
			}
		}
	}
	if worst != nil {
		return nil, worst
	}
	return out, nil
}

func touched(postings []Posting) []AccountRef {
	return Transaction{Postings: postings}.Accounts()
}

// Shortfall — сколько не хватает на сумму need при остатке available; ноль — хватает.
func Shortfall(available, need money.Kopecks) money.Kopecks {
	if available >= need {
		return 0
	}
	return need - available
}
