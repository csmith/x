package desktop

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen_refusesOptions(t *testing.T) {
	for _, url := range []string{"-aCalculator", "--version"} {
		require.Error(t, Open(url))
	}
}

func TestUTF16LE(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []byte
	}{
		{
			"empty",
			"",
			[]byte{0xff, 0xfe},
		},
		{
			"ascii",
			"hi",
			[]byte{0xff, 0xfe, 'h', 0, 'i', 0},
		},
		{
			"non-ascii",
			"é",
			[]byte{0xff, 0xfe, 0xe9, 0x00},
		},
		{
			"surrogate pair",
			"😀",
			[]byte{0xff, 0xfe, 0x3d, 0xd8, 0x00, 0xde},
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, utf16LE(tt.text))
			},
		)
	}
}
