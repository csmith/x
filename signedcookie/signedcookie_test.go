package signedcookie

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type payload struct {
	User string `json:"user"`
	N    int    `json:"n"`
}

func newSigner(t *testing.T, key string) *Signer {
	t.Helper()
	s, err := New([]byte(key))
	require.NoError(t, err)
	return s
}

func TestNew_rejectsShortKeys(t *testing.T) {
	_, err := New([]byte(strings.Repeat("k", MinKeyLength-1)))
	assert.Error(t, err)
}

func TestSealOpen_roundTrip(t *testing.T) {
	s := newSigner(t, strings.Repeat("k", 32))

	value, err := s.Seal("session", payload{User: "alice", N: 3})
	require.NoError(t, err)

	var got payload
	require.True(t, s.Open("session", value, &got))
	assert.Equal(t, payload{User: "alice", N: 3}, got)
}

func TestOpen_rejectsTampering(t *testing.T) {
	s := newSigner(t, strings.Repeat("k", 32))
	value, err := s.Seal("session", payload{User: "alice"})
	require.NoError(t, err)
	encoded, sig, _ := strings.Cut(value, ".")

	tests := map[string]struct {
		name  string
		value string
	}{
		"a changed payload": {
			"session",
			base64.RawURLEncoding.EncodeToString([]byte(`{"user":"mallory"}`)) + "." + sig,
		},
		"a truncated signature":       {"session", encoded + "." + sig[:len(sig)-2]},
		"no signature":                {"session", encoded},
		"an empty value":              {"session", ""},
		"a signature that is not b64": {"session", encoded + ".!!"},
		"another cookie's name":       {"other", value},
		"another key's signature": {
			"session",
			func() string {
				v, err := newSigner(t, strings.Repeat("j", 32)).Seal("session", payload{User: "alice"})
				require.NoError(t, err)
				return v
			}(),
		},
	}
	for name, tt := range tests {
		t.Run(
			name,
			func(t *testing.T) {
				var got payload
				assert.False(t, s.Open(tt.name, tt.value, &got))
			},
		)
	}
}

func TestOpen_rejectsSignedNonJSON(t *testing.T) {
	s := newSigner(t, strings.Repeat("k", 32))
	encoded := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	value := encoded + "." + base64.RawURLEncoding.EncodeToString(s.mac("session", encoded))

	var got payload
	assert.False(t, s.Open("session", value, &got))
}

func TestRead(t *testing.T) {
	s := newSigner(t, strings.Repeat("k", 32))
	value, err := s.Seal("session", payload{User: "alice"})
	require.NoError(t, err)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	var got payload
	assert.False(t, s.Read(r, "session", &got), "missing cookie")

	r.AddCookie(&http.Cookie{Name: "session", Value: value})
	require.True(t, s.Read(r, "session", &got))
	assert.Equal(t, "alice", got.User)
}
