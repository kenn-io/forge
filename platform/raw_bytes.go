package platform

import (
	"encoding/base64"
	"errors"
)

// RawBytes is the sole wire representation of a byte-preserving Git field.
// Base64 is always canonical padded RFC 4648, including for valid UTF-8.
type RawBytes struct {
	Base64 string `json:"base64" contentEncoding:"base64"`
}

func NewRawBytes(value []byte) RawBytes {
	return RawBytes{Base64: base64.StdEncoding.EncodeToString(value)}
}

func (value RawBytes) Bytes() ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value.Base64)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != value.Base64 {
		return nil, errors.New("noncanonical base64")
	}
	return decoded, nil
}
