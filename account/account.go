// Package account encodes and validates opaque issuer/subject account keys.
package account

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid_account_key")

type Principal struct{ Issuer, Subject string }

func (p Principal) Valid() bool {
	for _, s := range []string{p.Issuer, p.Subject} {
		if s == "" || len(s) > 1024 || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
			return false
		}
	}
	return base64.RawURLEncoding.EncodedLen(len(p.Issuer))+1+base64.RawURLEncoding.EncodedLen(len(p.Subject)) <= 2048
}
func (p Principal) Key() string {
	if !p.Valid() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(p.Issuer)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(p.Subject))
}
func Parse(key string) (Principal, error) {
	if len(key) > 2048 || strings.Count(key, ":") != 1 {
		return Principal{}, ErrInvalid
	}
	a, b, _ := strings.Cut(key, ":")
	i, err1 := base64.RawURLEncoding.Strict().DecodeString(a)
	s, err2 := base64.RawURLEncoding.Strict().DecodeString(b)
	p := Principal{string(i), string(s)}
	if err1 != nil || err2 != nil || !p.Valid() || p.Key() != key {
		return Principal{}, ErrInvalid
	}
	return p, nil
}
