package tessa

import (
	"encoding/json"
	"errors"
)

// A count-only request cannot reach synthesis with a page of rows, missing
// evidence, or an unavailable aggregate. Zero is a valid exact total.
func validateBookingCountEvidence(input SynthesisInput) error {
	if input.BookingCountOnly && len(input.Tools) == 0 {
		return errors.New("booking count requires aggregate evidence")
	}
	for index, tool := range input.Tools {
		isCount := tool.Name == "get_booking_metrics" && tool.Metric == "count"
		if input.BookingCountOnly && !isCount {
			return errors.New("count-only request cannot use list evidence")
		}
		if !isCount {
			continue
		}
		if index >= len(input.Evidence) || input.Evidence[index].Tool != tool.Name {
			return errors.New("booking count evidence is missing or mismatched")
		}
		var result struct {
			Total *int64 `json:"total_bookings"`
			Exact bool   `json:"exact"`
			From  string `json:"from"`
			To    string `json:"to"`
		}
		if err := json.Unmarshal(input.Evidence[index].Result, &result); err != nil || !result.Exact || result.Total == nil || *result.Total < 0 || result.From != tool.From || result.To != tool.To {
			return errors.New("booking count requires an exact nonnegative total for the requested range")
		}
	}
	if input.BookingCountOnly && len(input.Tools) != len(input.Evidence) {
		return errors.New("count-only request has unexpected evidence")
	}
	return nil
}
