// Package operatortoken parses and securely reads operator credentials.
package operatortoken

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
)

var (
	errInvalidToken = errors.New("invalid operator token")
	errInvalidFile  = errors.New("invalid operator token file")
)

func Parse(encoded string) ([32]byte, error) {
	if len(encoded) != 43 {
		return [32]byte{}, errInvalidToken
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, errInvalidToken
	}
	var token [32]byte
	copy(token[:], decoded)
	if token == ([32]byte{}) {
		return [32]byte{}, errInvalidToken
	}
	return token, nil
}

// ReadFile accepts a regular private 0600 file containing one canonical token,
// optionally terminated by LF or CRLF.
func ReadFile(path string) ([32]byte, error) {
	return readFileWith(path, os.Open)
}

func readFileWith(path string, open func(string) (*os.File, error)) ([32]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 {
		return [32]byte{}, errInvalidFile
	}
	file, err := open(path)
	if err != nil {
		return [32]byte{}, errInvalidFile
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !os.SameFile(before, opened) {
		return [32]byte{}, errInvalidFile
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 ||
		!os.SameFile(before, after) || !os.SameFile(opened, after) {
		return [32]byte{}, errInvalidFile
	}
	body, err := io.ReadAll(io.LimitReader(file, 46))
	if err != nil || len(body) > 45 {
		return [32]byte{}, errInvalidFile
	}
	switch {
	case len(body) == 43:
	case len(body) == 44 && body[43] == '\n':
	case len(body) == 45 && body[43] == '\r' && body[44] == '\n':
	default:
		return [32]byte{}, errInvalidFile
	}
	token, err := Parse(string(body[:43]))
	if err != nil {
		return [32]byte{}, errInvalidFile
	}
	return token, nil
}
