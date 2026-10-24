package operatortoken

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testToken = func() (token [32]byte) {
	for index := range token {
		token[index] = 'K'
	}
	return token
}()

func writeTokenFile(t *testing.T, suffix string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(testToken[:])+suffix), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseIsStrictAndRejectsZero(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(testToken[:])
	if got, err := Parse(valid); err != nil || got != testToken {
		t.Fatalf("Parse(valid) = (%x, %v)", got, err)
	}

	tests := []struct {
		name  string
		value string
	}{
		{"short", valid[:len(valid)-1]},
		{"long", valid + "A"},
		{"invalid alphabet", strings.Repeat("!", 43)},
		{"noncanonical trailing bits", valid[:42] + "t"},
		{"all zero", base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Parse(tt.value); err == nil || got != ([32]byte{}) {
				t.Fatalf("Parse(%q) = (%x, %v), want zero/error", tt.name, got, err)
			}
		})
	}
}

func TestReadFileContract(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run("accepted suffix "+strings.ReplaceAll(suffix, "\n", "LF"), func(t *testing.T) {
			path := writeTokenFile(t, suffix)
			if got, err := ReadFile(path); err != nil || got != testToken {
				t.Fatalf("ReadFile() = (%x, %v)", got, err)
			}
		})
	}

	valid := base64.RawURLEncoding.EncodeToString(testToken[:])
	zero := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	noncanonical := valid[:len(valid)-1] + "t"
	invalidBodies := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"short", valid[:len(valid)-1]},
		{"long", valid + "A"},
		{"invalid alphabet", strings.Repeat("!", 43)},
		{"noncanonical trailing bits", noncanonical},
		{"all zero", zero},
		{"trailing CR", valid + "\r"},
		{"two trailing LF", valid + "\n\n"},
		{"trailing space", valid + " "},
		{"over bounded read", valid + "\r\nX"},
	}
	for _, tt := range invalidBodies {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("WriteFile(): %v", err)
			}
			if token, err := ReadFile(path); err == nil || token != ([32]byte{}) {
				t.Fatalf("ReadFile() = (%x, %v), want zero/error", token, err)
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		if _, err := ReadFile(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("missing token file was accepted")
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		if _, err := ReadFile(t.TempDir()); err == nil {
			t.Fatal("directory token path was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := writeTokenFile(t, "")
		link := filepath.Join(t.TempDir(), "token-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(): %v", err)
		}
		if _, err := ReadFile(link); err == nil {
			t.Fatal("symlink token path was accepted")
		}
	})
	for _, mode := range []os.FileMode{0o400, 0o640, 0o644, 0o604, 0o666} {
		t.Run("mode "+mode.String(), func(t *testing.T) {
			path := writeTokenFile(t, "")
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("Chmod(): %v", err)
			}
			if _, err := ReadFile(path); err == nil {
				t.Fatalf("mode %04o was accepted", mode.Perm())
			}
		})
	}

	t.Run("ambiguous replacement", func(t *testing.T) {
		original := writeTokenFile(t, "")
		replacement := writeTokenFile(t, "")
		if _, err := readFileWith(original, func(string) (*os.File, error) {
			return os.Open(replacement)
		}); err == nil {
			t.Fatal("replacement between Lstat and Open was accepted")
		}
	})

	t.Run("same inode symlink replacement", func(t *testing.T) {
		original := writeTokenFile(t, "")
		moved := filepath.Join(filepath.Dir(original), "moved-token")
		if _, err := readFileWith(original, func(path string) (*os.File, error) {
			if err := os.Rename(path, moved); err != nil {
				return nil, err
			}
			if err := os.Symlink(moved, path); err != nil {
				return nil, err
			}
			return os.Open(path)
		}); err == nil {
			t.Fatal("same-inode symlink replacement between Lstat and Open was accepted")
		}
	})
}
