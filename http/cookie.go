// Package http has helpers for HTTP servers, such as signed cookies.
package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MinCookieKeyLength is the shortest key NewCookieSigner accepts.
const MinCookieKeyLength = 32

// CookieSigner keeps small values in cookies signed with HMAC-SHA256, so a
// server can trust what comes back without keeping a store. Values are signed
// but not encrypted: the client can read them, but not change them.
//
// The signature covers the cookie's name as well as its value, so a value
// sealed for one cookie is not accepted for another.
type CookieSigner struct {
	key []byte
}

// NewCookieSigner returns a CookieSigner using key, which must be at least
// MinCookieKeyLength bytes.
// Changing the key invalidates every value sealed with the old one.
func NewCookieSigner(key []byte) (*CookieSigner, error) {
	if len(key) < MinCookieKeyLength {
		return nil, fmt.Errorf("the cookie signing key must be at least %d bytes", MinCookieKeyLength)
	}
	return &CookieSigner{key: key}, nil
}

// Seal encodes v as JSON and signs it for the cookie called name. The result
// is safe to use as a cookie value.
func (s *CookieSigner) Seal(name string, v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(s.mac(name, encoded)), nil
}

// Open checks that value was sealed for the cookie called name and decodes
// it into v. It reports false if the signature or encoding is wrong, in
// which case v may have been partly written.
func (s *CookieSigner) Open(name, value string, v any) bool {
	encoded, sig, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, s.mac(name, encoded)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	return json.Unmarshal(payload, v) == nil
}

// Read opens the cookie called name from r into v, reporting false if it is
// missing or not validly sealed.
func (s *CookieSigner) Read(r *http.Request, name string, v any) bool {
	cookie, err := r.Cookie(name)
	if err != nil {
		return false
	}
	return s.Open(name, cookie.Value, v)
}

func (s *CookieSigner) mac(name, encoded string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(encoded))
	return h.Sum(nil)
}
