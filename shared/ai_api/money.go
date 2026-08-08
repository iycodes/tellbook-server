package aiapi

import (
	"encoding/json"
	"errors"
	"strconv"
)

// MinorAmount is quoted in JSON so every caller preserves signed 64-bit precision.
type MinorAmount int64

func (m MinorAmount) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(m), 10))
}

func (m *MinorAmount) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("cannot unmarshal minor amount into a nil receiver")
	}

	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("minor amount must be a quoted base-10 integer")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || strconv.FormatInt(parsed, 10) != value {
		return errors.New("minor amount must be a canonical signed 64-bit integer")
	}
	*m = MinorAmount(parsed)
	return nil
}
