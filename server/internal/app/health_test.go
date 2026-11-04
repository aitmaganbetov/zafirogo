package app

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/config"
)

func TestHealthOptions(t *testing.T) {
	cfg, err := config.Load("", func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	o := healthOptions(cfg.Agent.Health)
	if o.Disabled || o.Thresholds.CPUTempC != 90 || o.Thresholds.RaiseAfter != 2*time.Minute || o.LiveLoadStepBP != 500 ||
		o.LiveTempStepDeci != 20 || o.LiveMinGap != 15*time.Second || o.PingInterval != time.Minute {
		t.Errorf("defaults: %+v", o)
	}
	h := cfg.Agent.Health
	h.Enabled, h.PingInterval = false, 0
	if o := healthOptions(h); !o.Disabled || o.PingInterval >= 0 {
		t.Errorf("disabled / ping 0 (не мерить): %+v", o)
	}
}

func TestVerdictRules(t *testing.T) {
	cfg, err := config.Load("", func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if r := verdictRules(cfg.Agent.Health); r != health.DefaultVerdictRules() {
		t.Errorf("verdict rules from defaults = %+v, want %+v", r, health.DefaultVerdictRules())
	}
}
