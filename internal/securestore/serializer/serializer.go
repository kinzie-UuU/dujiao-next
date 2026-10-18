package serializer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/dujiao-next/internal/crypto"
	"gorm.io/gorm/schema"
)

const (
	StringSerializerName = "secure_string"
	JSONSerializerName   = "secure_json"
	ciphertextPrefix     = "enc:v1:"
)

var configureMu sync.Mutex

func init() {
	schema.RegisterSerializer(StringSerializerName, passthroughStringSerializer{})
	schema.RegisterSerializer(JSONSerializerName, passthroughJSONSerializer{})
}

func Configure(secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("secure storage key is empty")
	}
	configureMu.Lock()
	defer configureMu.Unlock()
	key := crypto.DeriveKey(secret)
	schema.RegisterSerializer(StringSerializerName, encryptedStringSerializer{key: key})
	schema.RegisterSerializer(JSONSerializerName, encryptedJSONSerializer{key: key})
	return nil
}

func IsEncryptedString(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), ciphertextPrefix)
}

func IsEncryptedJSON(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" {
		return false
	}
	if IsEncryptedString(value) {
		return true
	}
	var decoded string
	return json.Unmarshal([]byte(value), &decoded) == nil && IsEncryptedString(decoded)
}

type passthroughStringSerializer struct{}

func (passthroughStringSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	return setStringField(ctx, field, dst, databaseString(dbValue))
}

func (passthroughStringSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	value, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("secure string has unexpected type %T", fieldValue)
	}
	return value, nil
}

type passthroughJSONSerializer struct{}

func (passthroughJSONSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	return setJSONField(ctx, field, dst, []byte(databaseString(dbValue)))
}

func (passthroughJSONSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	encoded, err := json.Marshal(fieldValue)
	if err != nil {
		return nil, err
	}
	if string(encoded) == "null" {
		return nil, nil
	}
	return string(encoded), nil
}

type encryptedStringSerializer struct{ key []byte }

func (s encryptedStringSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	value := databaseString(dbValue)
	if value != "" && IsEncryptedString(value) {
		plain, err := crypto.Decrypt(s.key, strings.TrimPrefix(value, ciphertextPrefix))
		if err != nil {
			return fmt.Errorf("decrypt secure string: %w", err)
		}
		value = plain
	}
	return setStringField(ctx, field, dst, value)
}

func (s encryptedStringSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	value, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("secure string has unexpected type %T", fieldValue)
	}
	if value == "" {
		return "", nil
	}
	encrypted, err := crypto.Encrypt(s.key, value)
	if err != nil {
		return nil, fmt.Errorf("encrypt secure string: %w", err)
	}
	return ciphertextPrefix + encrypted, nil
}

type encryptedJSONSerializer struct{ key []byte }

func (s encryptedJSONSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	raw := []byte(databaseString(dbValue))
	if len(raw) == 0 || string(raw) == "null" {
		return setJSONField(ctx, field, dst, nil)
	}
	ciphertext := encodedCiphertext(raw)
	if ciphertext != "" {
		plain, err := crypto.Decrypt(s.key, strings.TrimPrefix(ciphertext, ciphertextPrefix))
		if err != nil {
			return fmt.Errorf("decrypt secure json: %w", err)
		}
		raw = []byte(plain)
	}
	return setJSONField(ctx, field, dst, raw)
}

func (s encryptedJSONSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	plain, err := json.Marshal(fieldValue)
	if err != nil {
		return nil, err
	}
	if string(plain) == "null" {
		return nil, nil
	}
	encrypted, err := crypto.Encrypt(s.key, string(plain))
	if err != nil {
		return nil, fmt.Errorf("encrypt secure json: %w", err)
	}
	encoded, err := json.Marshal(ciphertextPrefix + encrypted)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func databaseString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func encodedCiphertext(raw []byte) string {
	value := strings.TrimSpace(string(raw))
	if IsEncryptedString(value) {
		return value
	}
	var decoded string
	if json.Unmarshal(raw, &decoded) == nil && IsEncryptedString(decoded) {
		return decoded
	}
	return ""
}

func setStringField(ctx context.Context, field *schema.Field, dst reflect.Value, value string) error {
	return field.Set(ctx, dst, value)
}

func setJSONField(ctx context.Context, field *schema.Field, dst reflect.Value, raw []byte) error {
	value := reflect.New(field.FieldType)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, value.Interface()); err != nil {
			return err
		}
	}
	field.ReflectValueOf(ctx, dst).Set(value.Elem())
	return nil
}
