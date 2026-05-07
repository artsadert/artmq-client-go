// Demonstrates MQTT 5 shared subscriptions. Filter syntax:
//
//	$share/<group>/<filter>
//
// Subscribers in the same group form a worker pool: the broker delivers
// each message to exactly one of them (round-robin / load-balanced),
// instead of broadcasting to all subscribers.
package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	artmq "github.com/artsadert/artmq-client-go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Three workers share the same group "g1". Each message goes to one of them.
	const n = 3
	workers := make([]*artmq.Client, n)
	counts := make([]int64, n)
	for i := 0; i < n; i++ {
		idx := i
		workers[i] = artmq.NewClient(artmq.NewClientOptions().
			SetBrokerAddr("localhost:1883").
			SetClientID(fmt.Sprintf("worker-%d", idx)))
		if err := workers[i].Connect(ctx); err != nil {
			log.Fatalf("worker %d connect: %v", idx, err)
		}
		defer workers[i].Disconnect()
		if err := workers[i].Subscribe(ctx, "$share/g1/tasks", artmq.QoS1,
			func(topic string, payload []byte) {
				atomic.AddInt64(&counts[idx], 1)
				fmt.Printf("worker-%d picked: %s\n", idx, payload)
			}); err != nil {
			log.Fatalf("worker %d subscribe: %v", idx, err)
		}
	}

	pub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("dispatcher"))
	if err := pub.Connect(ctx); err != nil {
		log.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	time.Sleep(150 * time.Millisecond) // let SUBACKs settle on all workers

	const total = 9
	for i := 0; i < total; i++ {
		body := fmt.Sprintf("task-%02d", i)
		if err := pub.Publish(ctx, "tasks", []byte(body), artmq.WithQoS(1)); err != nil {
			log.Fatalf("publish %s: %v", body, err)
		}
	}

	time.Sleep(500 * time.Millisecond) // wait for delivery

	fmt.Println("---")
	var sum int64
	for i := range counts {
		v := atomic.LoadInt64(&counts[i])
		sum += v
		fmt.Printf("worker-%d processed %d task(s)\n", i, v)
	}
	fmt.Printf("total delivered: %d / %d (each task should land on exactly one worker)\n", sum, total)
}
