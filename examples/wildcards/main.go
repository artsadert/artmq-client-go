// Demonstrates MQTT topic wildcards.
//
//	+  matches exactly one topic level
//	#  matches zero or more trailing levels (must be the last segment)
//
// Two subscribers register different filters and receive the subset of
// published topics that their filter matches.
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

	sub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("wild-sub"))
	pub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("wild-pub"))

	if err := sub.Connect(ctx); err != nil {
		log.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()
	if err := pub.Connect(ctx); err != nil {
		log.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	// "+" — single-level wildcard. Matches sensors/<anything>/temp but not
	// sensors/floor1/room2/temp.
	if err := sub.Subscribe(ctx, "sensors/+/temp", artmq.QoS0,
		func(topic string, payload []byte) {
			fmt.Printf("[+/temp] %s = %s\n", topic, payload)
		}); err != nil {
		log.Fatalf("subscribe single-level: %v", err)
	}

	// "#" — multi-level wildcard. Matches sensors/<any depth>.
	if err := sub.Subscribe(ctx, "sensors/#", artmq.QoS0,
		func(topic string, payload []byte) {
			fmt.Printf("[sensors/#] %s = %s\n", topic, payload)
		}); err != nil {
		log.Fatalf("subscribe multi-level: %v", err)
	}

	// Both subscriptions also fire for the same topic when both match —
	// that's expected MQTT behaviour and useful for cross-cutting consumers.

	time.Sleep(100 * time.Millisecond) // let the broker register the filters

	for _, topic := range []string{
		"sensors/floor1/temp",         // matches both
		"sensors/floor1/humidity",     // matches sensors/# only
		"sensors/floor1/room2/temp",   // matches sensors/# only (3 levels)
		"unrelated/topic",             // matches nothing
	} {
		if err := pub.Publish(ctx, topic, []byte("42"), artmq.WithQoS(0)); err != nil {
			log.Fatalf("publish %s: %v", topic, err)
		}
	}

	// Settle delivery before exit.
	time.Sleep(200 * time.Millisecond)
}
