package mail

import (
	"testing"
	"time"
)

func TestReduceDelivery(t *testing.T) {
	now := time.Now()
	if actual := ReduceDelivery("sent", now, "email.delivered", now.Add(time.Second)); actual != "delivered" {
		t.Fatalf("got %q", actual)
	}
	if actual := ReduceDelivery("delivered", now, "email.sent", now.Add(2*time.Second)); actual != "delivered" {
		t.Fatalf("terminal state regressed to %q", actual)
	}
	if actual := ReduceDelivery("bounced", now, "email.delivered", now.Add(time.Second)); actual != "bounced" {
		t.Fatalf("terminal precedence changed to %q", actual)
	}
}
