package jsonmap

import (
	"reflect"
	"testing"
)

func TestJSONDatabaseRoundTrip(t *testing.T) {
	original := JSON{
		"enabled": true,
		"name":    "storefront",
		"nested":  map[string]interface{}{"count": float64(2)},
	}

	encoded, err := original.Value()
	if err != nil {
		t.Fatalf("encode JSON value: %v", err)
	}

	var decoded JSON
	if err := decoded.Scan(encoded); err != nil {
		t.Fatalf("scan JSON value: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\ngot:  %#v", original, decoded)
	}
}

func TestJSONScanNilCreatesEmptyObject(t *testing.T) {
	value := JSON{"stale": true}
	if err := value.Scan(nil); err != nil {
		t.Fatalf("scan nil JSON: %v", err)
	}
	if value == nil || len(value) != 0 {
		t.Fatalf("scan nil should create an empty object, got %#v", value)
	}
}

func TestJSONNilDatabaseValueStaysNull(t *testing.T) {
	var value JSON
	encoded, err := value.Value()
	if err != nil {
		t.Fatalf("encode nil JSON: %v", err)
	}
	if encoded != nil {
		t.Fatalf("nil JSON should produce a NULL driver value, got %#v", encoded)
	}
}

func TestJSONScanReplacesPreviousObject(t *testing.T) {
	for name, source := range map[string]interface{}{
		"text": `{"enabled":true,"nested":{"count":2}}`,
		"blob": []byte(`{"enabled":true,"nested":{"count":2}}`),
	} {
		t.Run(name, func(t *testing.T) {
			value := JSON{"stale": true}
			if err := value.Scan(source); err != nil {
				t.Fatal(err)
			}
			want := JSON{"enabled": true, "nested": map[string]interface{}{"count": float64(2)}}
			if !reflect.DeepEqual(value, want) {
				t.Fatalf("scan mismatch: want %#v, got %#v", want, value)
			}
		})
	}
}

func TestJSONScanRejectsInvalidValuesWithoutRetainingState(t *testing.T) {
	for name, source := range map[string]interface{}{
		"unsupported":    int64(42),
		"malformed_text": `{"enabled":`,
		"malformed_blob": []byte(`{"enabled":`),
		"array":          `[1,2]`,
		"scalar":         `true`,
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			value := JSON{"stale": true}
			if err := value.Scan(source); err == nil {
				t.Fatal("invalid database value was accepted")
			}
			if value != nil {
				t.Fatalf("invalid scan retained state: %#v", value)
			}
		})
	}
}

func TestJSONScanJSONNull(t *testing.T) {
	for _, source := range []interface{}{`null`, []byte(`null`)} {
		value := JSON{"stale": true}
		if err := value.Scan(source); err != nil {
			t.Fatal(err)
		}
		if value != nil {
			t.Fatalf("JSON null should clear state, got %#v", value)
		}
	}
}
