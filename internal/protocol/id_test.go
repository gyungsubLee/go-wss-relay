package protocol

import (
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "one byte", id: "a", want: true},
		{name: "64 bytes", id: strings.Repeat("a", 64), want: true},
		{name: "later punctuation", id: "a._-", want: true},
		{name: "empty", id: "", want: false},
		{name: "65 bytes", id: strings.Repeat("a", 65), want: false},
		{name: "leading dot", id: ".a", want: false},
		{name: "leading underscore", id: "_a", want: false},
		{name: "leading hyphen", id: "-a", want: false},
		{name: "slash", id: "a/b", want: false},
		{name: "space", id: "a b", want: false},
		{name: "non ASCII", id: "룸", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidID(tt.id); got != tt.want {
				t.Fatalf("ValidID(%q) = %t, want %t", tt.id, got, tt.want)
			}
		})
	}
}
