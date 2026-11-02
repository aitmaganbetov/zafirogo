package commands

import (
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

func TestKindOfCoversEveryCommand(t *testing.T) {
	// Каждое поле oneof Command.kind должно иметь свой Kind (новые команды в proto — сюда же).
	oneof := (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind")
	seen := map[Kind]bool{}
	for i := 0; i < oneof.Fields().Len(); i++ {
		f := oneof.Fields().Get(i)
		c := &agentv1.Command{}
		m := c.ProtoReflect()
		m.Set(f, m.NewField(f))
		k := KindOf(c)
		if string(k) != string(f.Name()) {
			t.Errorf("KindOf(%s) = %q, want %q", f.Name(), k, f.Name())
		}
		seen[k] = true
	}
	if KindOf(&agentv1.Command{}) != "" {
		t.Error("empty command must have no kind")
	}
}

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		kind    Kind
		durable bool
		coal    bool
	}{
		{KindStartSession, true, false},
		{KindEndSession, true, false},
		{KindReboot, true, false},
		{KindShutdown, true, false},
		{KindShowMessage, true, false},
		{KindWakeOnLan, true, false},
		{KindEnterServiceMode, true, false},
		{KindUpdateAvailable, true, false},
		{KindSignOutGuest, true, false},
		{KindLock, true, false},
		{KindUnlock, true, false},
		{KindBalanceChanged, false, true},
		{KindGuestCountersChanged, false, true},
		{KindPing, false, false},
		{KindChatMessageReceived, false, false},
		{KindTranslationsChanged, false, false},
		{KindConfigChanged, false, false},
		{KindQrLoginApproved, false, false},
		{KindDrain, false, false},
		{KindAutoBillingChanged, false, false},
	}
	for _, c := range cases {
		p := PolicyFor(c.kind)
		if p.Durable != c.durable || p.Coalesce != c.coal {
			t.Errorf("%s: durable=%v coalesce=%v, want %v %v", c.kind, p.Durable, p.Coalesce, c.durable, c.coal)
		}
	}
	// Laravel: app/Services/ComputerPower.php COMMAND_TTL_SECONDS = 120, MESSAGE_TTL_SECONDS = 300.
	if PolicyFor(KindReboot).TTL.Seconds() != 120 || PolicyFor(KindShowMessage).TTL.Seconds() != 300 {
		t.Error("power/message TTL must match ComputerPower")
	}
}

func TestSupported(t *testing.T) {
	none := presence.Capabilities{}
	if Supported(KindReboot, none) || Supported(KindShowMessage, none) || Supported(KindWakeOnLan, none) ||
		Supported(KindEnterServiceMode, none) || Supported(KindUpdateAvailable, none) {
		t.Error("commands that need a capability must be unsupported without it")
	}
	if !Supported(KindStartSession, none) || !Supported(KindBalanceChanged, none) {
		t.Error("session and balance commands need no capability")
	}
	if !Supported(KindReboot, presence.Capabilities{PowerControl: true}) {
		t.Error("power_control enables reboot")
	}
}

func TestValidate(t *testing.T) {
	ok := []*agentv1.Command{
		{Kind: &agentv1.Command_ShowMessage{ShowMessage: &agentv1.ShowMessage{Text: strings.Repeat("я", MaxMessageRunes)}}},
		{Kind: &agentv1.Command_WakeOnLan{WakeOnLan: &agentv1.WakeOnLan{MacAddresses: []string{"AA:BB:CC:DD:EE:FF"}}}},
		{Kind: &agentv1.Command_Ping{Ping: &agentv1.Ping{}}},
	}
	for _, c := range ok {
		if err := Validate(c); err != nil {
			t.Errorf("Validate(%s): %v", KindOf(c), err)
		}
	}
	bad := []*agentv1.Command{
		nil,
		{},
		{Kind: &agentv1.Command_ShowMessage{ShowMessage: &agentv1.ShowMessage{Text: strings.Repeat("я", MaxMessageRunes+1)}}},
		{Kind: &agentv1.Command_ShowMessage{ShowMessage: &agentv1.ShowMessage{}}},
		{Kind: &agentv1.Command_WakeOnLan{WakeOnLan: &agentv1.WakeOnLan{MacAddresses: make([]string, MaxWakeTargets+1)}}},
		{Kind: &agentv1.Command_StartSession{StartSession: &agentv1.StartSession{}}},
	}
	for i, c := range bad {
		if err := Validate(c); !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("bad[%d]: want ErrInvalidCommand, got %v", i, err)
		}
	}
}

func TestInstanceSubject(t *testing.T) {
	if !ValidInstance("api-1_a") || ValidInstance("a.b") || ValidInstance("") || ValidInstance("a b") || ValidInstance("*") {
		t.Error("ValidInstance")
	}
	if got := InstanceSubject("", "x"); got != "zafiro.agent.inst.x" {
		t.Errorf("InstanceSubject = %q", got)
	}
}
