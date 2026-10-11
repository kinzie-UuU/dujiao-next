// Package jsonmap provides the shared JSON object value used at persistence
// and transport boundaries. It is deliberately independent from GORM models
// and business modules so bounded contexts do not need to import a global
// models package merely to exchange arbitrary JSON objects.
package jsonmap

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON is a JSON object with database/sql serialization support.
type JSON map[string]interface{}

// Value implements driver.Valuer.
func (value JSON) Value() (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

// Scan implements sql.Scanner.
func (value *JSON) Scan(source interface{}) error {
	// Scan replaces the whole persisted object, including on decoding failure.
	*value = nil
	if source == nil {
		*value = make(JSON)
		return nil
	}
	var data []byte
	switch source := source.(type) {
	case []byte:
		data = source
	case string:
		data = []byte(source)
	default:
		return fmt.Errorf("jsonmap: unsupported database value type %T", source)
	}
	var decoded JSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*value = decoded
	return nil
}
