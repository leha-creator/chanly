package tbank

import (
	"encoding/json"
	"time"
)

type party struct {
	Account string `json:"acct"`
	Name    string `json:"name"`
	INN     string `json:"inn"`
}
type operation struct {
	ID         string      `json:"operationId"`
	Date       time.Time   `json:"operationDate"`
	Status     string      `json:"operationStatus"`
	Account    string      `json:"accountNumber"`
	Type       string      `json:"typeOfOperation"`
	Currency   string      `json:"accountCurrencyDigitalCode"`
	Amount     json.Number `json:"accountAmount"`
	DocDate    *time.Time  `json:"docDate"`
	ChargeDate *time.Time  `json:"chargeDate"`
	Number     string      `json:"documentNumber"`
	Purpose    string      `json:"payPurpose"`
	Payer      *party      `json:"payer"`
	Receiver   *party      `json:"receiver"`
}
type balances struct {
	Opening json.Number `json:"balanceBegin"`
	Closing json.Number `json:"balanceEnd"`
	Credit  json.Number `json:"credit"`
	Debit   json.Number `json:"debit"`
}
type result struct {
	Operations []operation `json:"operations"`
	NextCursor string      `json:"nextCursor"`
	Balances   *balances   `json:"balances"`
}
