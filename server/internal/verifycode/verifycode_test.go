package verifycode

import (
	"testing"
	"time"
)

func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"+7 700 123-45-67": "+77001234567", "8 700 123 45 67": "+77001234567", "700 123-45-67": "+77001234567",
		"": "", "abc": "", "12": "+712",
	} {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

// RESEND_SECONDS - (int) diffInSeconds, не меньше 1; 5 кодов за час.
func TestLimits(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	epoch := time.Unix(0, 0)
	if e := limits("phone", now, epoch, 0, epoch); e != nil {
		t.Fatalf("no codes: %v", e)
	}
	e := limits("phone", now, now.Add(-1500*time.Millisecond), 1, now)
	if e == nil || e.Kind != KindWait || e.Params["seconds"] != "59" || e.Field != "phone" {
		t.Fatalf("wait: %+v", e)
	}
	if e := limits("code", now, now.Add(-59*time.Second-900*time.Millisecond), 1, now); e == nil || e.Params["seconds"] != "1" {
		t.Fatalf("min 1: %+v", e)
	}
	if e := limits("code", now, now.Add(-61*time.Second), 4, now.Add(-50*time.Minute)); e != nil {
		t.Fatalf("four in an hour: %v", e)
	}
	e = limits("code", now, now.Add(-61*time.Second), 5, now.Add(-50*time.Minute))
	if e == nil || e.Kind != KindHourly || e.RetryAfter != 10*time.Minute || e.Error() != MsgHourly {
		t.Fatalf("hourly: %+v", e)
	}
}

func TestNewCode(t *testing.T) {
	for range 1000 {
		c, err := newCode()
		if err != nil || !CodeRe.MatchString(c) || c[0] == '0' {
			t.Fatalf("code %q, %v", c, err)
		}
	}
}
