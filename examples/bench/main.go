// bench is a local sanity-check publisher harness. It opens a single SDK
// connection, fans out N goroutines that call Publish in a tight loop for a
// fixed duration, then prints throughput and per-call latency percentiles.
//
// Use the cross-repo benchmark suite for real numbers; this exists so you
// can spot regressions without leaving the SDK repo.
//
// Example:
//
//	go run ./examples/bench --addr localhost:1883 --qos 1 --concurrency 64 \
//	    --duration 10s --size 1024
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	artmq "github.com/artsadert/artmq-client-go"
)

func main() {
	var (
		addr        = flag.String("addr", "localhost:1883", "broker address (host:port)")
		topic       = flag.String("topic", "bench/throughput", "topic to publish to")
		qos         = flag.Int("qos", 0, "QoS level (0, 1, or 2)")
		concurrency = flag.Int("concurrency", 8, "number of concurrent publisher goroutines")
		duration    = flag.Duration("duration", 10*time.Second, "test duration")
		size        = flag.Int("size", 64, "payload size in bytes")
		warmup      = flag.Duration("warmup", 1*time.Second, "discard samples for this long after start")
	)
	flag.Parse()

	if *qos < 0 || *qos > 2 {
		fmt.Fprintln(os.Stderr, "qos must be 0, 1, or 2")
		os.Exit(2)
	}

	payload := make([]byte, *size)
	rand.New(rand.NewSource(1)).Read(payload)

	c := artmq.NewClient(artmq.NewClientOptions().
		SetBrokerAddr(*addr).
		SetClientID(fmt.Sprintf("bench-%d", time.Now().UnixNano())).
		SetKeepAlive(30 * time.Second).
		SetMaxInflight(*concurrency * 4))

	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Disconnect()

	var sent atomic.Int64
	perWorkerLatencies := make([][]time.Duration, *concurrency)
	for i := range perWorkerLatencies {
		perWorkerLatencies[i] = make([]time.Duration, 0, 65536)
	}

	start := time.Now()
	warmupUntil := start.Add(*warmup)
	deadline := start.Add(*duration)

	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			for {
				now := time.Now()
				if !now.Before(deadline) {
					return
				}
				t := time.Now()
				if err := c.Publish(ctx, *topic, payload, artmq.WithQoS(byte(*qos))); err != nil {
					log.Printf("worker %d publish: %v", idx, err)
					return
				}
				lat := time.Since(t)
				if t.After(warmupUntil) {
					perWorkerLatencies[idx] = append(perWorkerLatencies[idx], lat)
					sent.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(warmupUntil)

	var all []time.Duration
	for _, l := range perWorkerLatencies {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	pctl := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		idx := int(float64(len(all)) * p)
		if idx >= len(all) {
			idx = len(all) - 1
		}
		return all[idx]
	}

	rate := float64(sent.Load()) / elapsed.Seconds()
	fmt.Printf("addr=%s topic=%s qos=%d concurrency=%d duration=%s size=%dB\n",
		*addr, *topic, *qos, *concurrency, elapsed.Round(time.Millisecond), *size)
	fmt.Printf("sent     %d messages\n", sent.Load())
	fmt.Printf("rate     %.0f msg/s\n", rate)
	fmt.Printf("latency  p50=%s  p95=%s  p99=%s  max=%s\n",
		pctl(0.50).Round(time.Microsecond),
		pctl(0.95).Round(time.Microsecond),
		pctl(0.99).Round(time.Microsecond),
		pctl(1.00).Round(time.Microsecond))
}
