package artmq

import "strings"

// MatchTopic reports whether topic matches the MQTT subscription filter.
// Supports + (single-level wildcard) and # (multi-level, must be last).
func MatchTopic(filter, topic string) bool {
	if filter == topic {
		return true
	}
	fp := strings.Split(filter, "/")
	tp := strings.Split(topic, "/")
	for i, f := range fp {
		if i >= len(tp) {
			return f == "#"
		}
		if f == "+" {
			continue
		}
		if f == "#" {
			return i == len(fp)-1
		}
		if f != tp[i] {
			return false
		}
	}
	return len(fp) == len(tp)
}
