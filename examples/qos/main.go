// Demonstrates each MQTT QoS level and what the SDK does for you.
//
//	QoS 0  fire-and-forget. Publish returns as soon as bytes are written.
//	QoS 1  at-least-once. Publish blocks until PUBACK is received.
//	QoS 2  exactly-once.   Publish performs the PUBREC/PUBREL/PUBCOMP handshake.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	artmq "github.com/artsadert/artmq-client-go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("qos-sub"))
	pub := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr("localhost:1883").SetClientID("qos-pub"))

	if err := sub.Connect(ctx); err != nil {
		log.Fatalf("sub connect: %v", err)
	}
	defer sub.Disconnect()
	if err := pub.Connect(ctx); err != nil {
		log.Fatalf("pub connect: %v", err)
	}
	defer pub.Disconnect()

	var wg sync.WaitGroup
	wg.Add(3)
	if err := sub.Subscribe(ctx, "qos/#", artmq.QoS2,
		func(topic string, payload []byte) {
			fmt.Printf("recv %-9s %s\n", topic, payload)
			wg.Done()
		}); err != nil {
		log.Fatalf("subscribe: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// QoS 0 — Publish returns immediately after the TCP write.
	t0 := time.Now()
	if err := pub.Publish(ctx, "qos/zero", []byte("fire-and-forget"),
		artmq.WithQoS(0)); err != nil {
		log.Fatalf("qos0: %v", err)
	}
	fmt.Printf("send qos0    in %s\n", time.Since(t0))

	// QoS 1 — Publish waits for PUBACK.
	t1 := time.Now()
	if err := pub.Publish(ctx, "qos/one", []byte("at-least-once"),
		artmq.WithQoS(1)); err != nil {
		log.Fatalf("qos1: %v", err)
	}
	fmt.Printf("send qos1    in %s (incl. PUBACK)\n", time.Since(t1))

	// QoS 2 — Publish waits for PUBREC, sends PUBREL, waits for PUBCOMP.
	t2 := time.Now()
	if err := pub.Publish(ctx, "qos/two", []byte("exactly-once"),
		artmq.WithQoS(2)); err != nil {
		log.Fatalf("qos2: %v", err)
	}
	fmt.Printf("send qos2    in %s (incl. PUBREC+PUBREL+PUBCOMP)\n", time.Since(t2))

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		log.Fatalf("timed out waiting for delivery")
	}
}
