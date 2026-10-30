package i18n

import "testing"

func TestETagMatch(t *testing.T) {
	for h, want := range map[string]bool{`"a"`: true, `W/"a"`: true, `"b", "a"`: true, `*`: true, `"b"`: false, ``: false} {
		if got := etagMatch(h, `"a"`); got != want {
			t.Errorf("%q: %v", h, got)
		}
	}
}
