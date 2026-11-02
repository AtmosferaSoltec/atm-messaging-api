package email

import (
	"encoding/json"
	"fmt"
)

func marshalData(d Data) ([]byte, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode email data: %w", err)
	}
	return raw, nil
}

func unmarshalData(raw []byte, d *Data) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, d); err != nil {
		return fmt.Errorf("decode email data: %w", err)
	}
	return nil
}
