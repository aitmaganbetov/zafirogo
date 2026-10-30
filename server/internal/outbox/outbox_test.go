package outbox

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateAndSubject(t *testing.T) {
	for _, typ := range []string{"computer.state", "shop_order.created", "computer.session.expired"} {
		if err := Validate(1, typ); err != nil {
			t.Errorf("%q: %v", typ, err)
		}
	}
	for _, typ := range []string{"", "computer", "Computer.state", "computer.", "club.>", "a.*", "computer state"} {
		if err := Validate(1, typ); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%q must be invalid", typ)
		}
	}
	if err := Validate(0, "a.b"); !errors.Is(err, ErrInvalidEvent) {
		t.Error("club 0 must be invalid")
	}
	if Subject(12, "chat.message") != "club.12.chat.message" || ClubSubjects(12) != "club.12.>" {
		t.Error("subject format")
	}
}

func TestMarshalData(t *testing.T) {
	b, err := marshalData(nil)
	if err != nil || string(b) != `{}` {
		t.Fatalf("nil: %s %v", b, err)
	}
	b, err = marshalData(struct {
		ComputerID int64 `json:"computer_id"`
	}{7})
	if err != nil || string(b) != `{"computer_id":7}` {
		t.Fatalf("struct: %s %v", b, err)
	}
	for _, bad := range []any{[]int{1}, "x", json.RawMessage(`null`), 5} {
		if _, err := marshalData(bad); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%v must be rejected", bad)
		}
	}
}

func TestDecodeEnvelope(t *testing.T) {
	env, err := DecodeEnvelope([]byte(`{"id":"e","type":"chat.message","club_id":3,"created_at":"2026-10-30T10:00:00Z"}`))
	if err != nil || string(env.Data) != `{}` || env.ClubID != 3 {
		t.Fatalf("%+v %v", env, err)
	}
	if _, err := DecodeEnvelope([]byte(`{"type":"chat.message","club_id":3}`)); err == nil {
		t.Fatal("envelope without id must be rejected")
	}
}
