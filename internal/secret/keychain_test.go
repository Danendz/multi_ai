package secret

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Touches the real login Keychain; opt in with AI_SWITCH_KEYCHAIN_TEST=1.
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("AI_SWITCH_KEYCHAIN_TEST") != "1" {
		t.Skip("set AI_SWITCH_KEYCHAIN_TEST=1 to run against the real Keychain")
	}
	k := Keychain{}
	const svc, acct = "ai-switch-test", "roundtrip"
	defer k.Delete(svc, acct)

	big := `{"v":"` + strings.Repeat("x", 10000) + `"}` // larger than `security -i`'s line limit
	for _, v := range []string{`{"a":1}`, big, `{"a":2}`} {
		if err := k.Set(svc, acct, []byte(v)); err != nil {
			t.Fatal(err)
		}
		got, err := k.Get(svc, acct)
		if err != nil || string(got) != v {
			t.Fatalf("round trip mismatch (len %d vs %d): %v", len(got), len(v), err)
		}
	}
	if err := k.Delete(svc, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(svc, acct); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
