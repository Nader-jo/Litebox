package mail

import "time"

var deliveryRank = map[string]int{
	"queued": 0, "submitted": 1, "sent": 2, "delivery_delayed": 3,
	"delivered": 10, "suppressed": 11, "bounced": 12, "failed": 13, "complained": 14,
}

// ReduceDelivery applies explicit precedence while retaining an existing terminal state.
func ReduceDelivery(current string, currentAt time.Time, event string, eventAt time.Time) string {
	event = trimEmailPrefix(event)
	if _, known := deliveryRank[event]; !known {
		return current
	}
	if current == "" {
		return event
	}
	currentRank, currentKnown := deliveryRank[current]
	eventRank := deliveryRank[event]
	if !currentKnown {
		return event
	}
	if currentRank >= 10 {
		if eventRank >= 10 && eventAt.After(currentAt) && eventRank > currentRank {
			return event
		}
		return current
	}
	if eventRank >= currentRank || eventAt.After(currentAt) {
		return event
	}
	return current
}

func trimEmailPrefix(value string) string {
	if len(value) > 6 && value[:6] == "email." {
		return value[6:]
	}
	return value
}
