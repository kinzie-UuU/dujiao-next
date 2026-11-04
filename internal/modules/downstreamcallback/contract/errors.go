package contract

import "errors"

var (
	ErrDuplicateRef = errors.New("downstream order reference already exists")
	ErrRefNotFound  = errors.New("downstream order ref not found")
	ErrInvalidRef   = errors.New("invalid downstream order ref")
)
