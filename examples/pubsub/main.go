// Demo: connects two clients to artmq, subscribes one, publishes from the
// other across QoS 0/1/2, and exits after three messages are received.
//
// Run:
//
//	# in one terminal
//	go run ./cmd/artmq           # from artmq-server
//
//	# in another terminal, from artmq-client-go
//	go run ./examples/pubsub
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
	subOpts := artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").
		SetClientID("demo-sub").
		SetKeepAlive(15 * time.Second)
	pubOpts := artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").
		SetClientID("demo-pub").
		SetKeepAlive(15 * time.Second)

	sub := artmq.NewClient(subOpts)
	pub := artmq.NewClient(pubOpts)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sub.Connect(ctx); err != nil {
		log.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()
	if err := pub.Connect(ctx); err != nil {
		log.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	var got int64
	done := make(chan struct{})
	if err := sub.Subscribe(ctx, "demo/+", artmq.QoS1, func(topic string, payload []byte) {
		fmt.Printf("recv %s: %s\n", topic, payload)
		if atomic.AddInt64(&got, 1) >= 3 {
			close(done)
		}
	}); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	// Allow the broker to register the subscription before publishing.
	time.Sleep(100 * time.Millisecond)

	if err := pub.Publish(ctx, "demo/qos0", []byte("hello at qos0"), artmq.WithQoS(0)); err != nil {
		log.Fatalf("publish qos0: %v", err)
	}
	if err := pub.Publish(ctx, "demo/qos1", []byte("hello at qos1"),
		artmq.WithQoS(1), artmq.WithPriority(5)); err != nil {
		log.Fatalf("publish qos1: %v", err)
	}
	if err := pub.Publish(ctx, "demo/qos2", []byte("hello at qos2"),
		artmq.WithQoS(2), artmq.WithTTL(60*time.Second)); err != nil {
		log.Fatalf("publish qos2: %v", err)
	}

	select {
	case <-done:
		fmt.Println("ok")
	case <-ctx.Done():
		log.Fatalf("timed out (got=%d)", atomic.LoadInt64(&got))
	}
}
