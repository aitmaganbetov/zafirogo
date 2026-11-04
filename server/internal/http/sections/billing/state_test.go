package billing

import (
	"testing"
	"time"

	billingapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/billing"
)

// ClubSubscription::isActive() / isPastPaidPeriod() → строка «Статус» billing/index.blade.php.
func TestState(t *testing.T) {
	now := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, c := range []struct {
		status        string
		trial, period *time.Time
		want          billingapi.BillingSubscriptionState
	}{
		{"active", nil, nil, billingapi.BillingSubscriptionStateActive},
		{"active", nil, &future, billingapi.BillingSubscriptionStateActive},
		{"active", nil, &past, billingapi.BillingSubscriptionStateExpired},
		{"expired", nil, &past, billingapi.BillingSubscriptionStateExpired},
		{"expired", nil, nil, billingapi.BillingSubscriptionStateInactive},
		{"suspended", nil, &past, billingapi.BillingSubscriptionStateInactive},
		{"trialing", &future, nil, billingapi.BillingSubscriptionStateTrialing},
		{"trialing", &past, nil, billingapi.BillingSubscriptionStateInactive},
	} {
		if got := State(c.status, c.trial, c.period, now); got != c.want {
			t.Errorf("%s %v %v: %s, want %s", c.status, c.trial, c.period, got, c.want)
		}
	}
}
