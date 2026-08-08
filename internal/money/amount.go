package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

var ErrInvalidCurrencyCode = errors.New("currency code must contain exactly three uppercase ASCII letters")

// Minor marshals as a quoted integer so JavaScript clients cannot lose precision.
type Minor int64

func (m Minor) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(m), 10))
}

func (m *Minor) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("cannot unmarshal minor units into a nil receiver")
	}
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return errors.New("minor units must be encoded as a quoted base-10 integer")
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == "" || value[0] == '+' || bytes.IndexAny([]byte(value), " \t\r\n") >= 0 {
		return errors.New("minor units must be a canonical base-10 integer")
	}
	if value == "-0" ||
		(len(value) > 1 && value[0] == '0') ||
		(len(value) > 2 && value[0] == '-' && value[1] == '0') {
		return errors.New("minor units must be a canonical base-10 integer")
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return errors.New("minor units must be a signed 64-bit base-10 integer")
	}
	*m = Minor(parsed)
	return nil
}

type Amount struct {
	AmountMinor  Minor  `json:"amount_minor"`
	CurrencyCode string `json:"currency_code"`
}

func NewAmount(amountMinor int64, currencyCode string) (Amount, error) {
	if !isCurrencyCode(currencyCode) {
		return Amount{}, ErrInvalidCurrencyCode
	}
	return Amount{
		AmountMinor:  Minor(amountMinor),
		CurrencyCode: currencyCode,
	}, nil
}

func isCurrencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}
