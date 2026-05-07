// Demonstrates artmq-specific PUBLISH properties:
//
//	WithPriority(n)  sets the "priority" user property. The artmq broker
//	                 stores messages in a priority queue ordered by ascending
//	                 priority value: lower number = served sooner (think Unix
//	                 nice). Equal priorities preserve insertion order.
//	WithTTL(d)       sets MQTT 5 Message Expiry Interval; the broker drops
//	                 messages that exceed this lifetime before delivery.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	artmq "github.com/artsadert/artmq-client-go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("prio-pub"))
	if err := pub.Connect(ctx); err != nil {
		log.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	// Push three messages onto an empty topic before any consumer connects.
	// When a consumer subscribes, the broker drains by priority order
	// (lower priority value = delivered first).
	for _, m := range []struct {
		body string
		prio int64
	}{
		{"low-priority background job", 10},
		{"normal-priority work", 5},
		{"urgent: pager fired", 1},
	} {
		if err := pub.Publish(ctx, "jobs", []byte(m.body),
			artmq.WithQoS(1),
			artmq.WithPriority(m.prio),
			artmq.WithTTL(60*time.Second),
		); err != nil {
			log.Fatalf("publish: %v", err)
		}
	}
	pub.Disconnect()

	// Now connect a consumer; expect highest priority first.
	sub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("prio-sub"))
	if err := sub.Connect(ctx); err != nil {
		log.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()

	count := 0
	done := make(chan struct{})
	if err := sub.Subscribe(ctx, "jobs", artmq.QoS1,
		func(topic string, payload []byte) {
			fmt.Printf("worker pulled: %s\n", payload)
			count++
			if count == 3 {
				close(done)
			}
		}); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	select {
	case <-done:
	case <-ctx.Done():
		log.Fatalf("timed out (got %d/3)", count)
	}
}
