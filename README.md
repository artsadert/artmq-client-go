# artmq-client-go

Go SDK for the [artmq](../artmq-server) message broker. The broker speaks
MQTT 5.0 over TCP; this SDK wraps the wire protocol behind a small Paho-style
API.

## Install

```bash
go get github.com/artsadert/artmq-client-go
```

## Quick start

```go
import (
    "context"
    "time"

    artmq "github.com/artsadert/artmq-client-go"
)

opts := artmq.NewClientOptions().
    SetBrokerAddr("localhost:1883").
    SetClientID("my-service").
    SetKeepAlive(30 * time.Second)

c := artmq.NewClient(opts)
ctx := context.Background()

if err := c.Connect(ctx); err != nil { panic(err) }
defer c.Disconnect()

c.Subscribe(ctx, "events/#", artmq.QoS1, func(topic string, payload []byte) {
    fmt.Println(topic, string(payload))
})

c.Publish(ctx, "events/hello", []byte("hi"),
    artmq.WithQoS(1),
    artmq.WithPriority(5),                    // user property "priority"
    artmq.WithTTL(60*time.Second),            // Message Expiry Interval
)
```

## Features

- MQTT 5.0 CONNECT / PUBLISH / SUBSCRIBE / PINGREQ / DISCONNECT
- QoS 0, 1 (PUBACK), and 2 (PUBREC/PUBREL/PUBCOMP)
- Wildcard subscriptions (`+`, `#`)
- MQTT 5 shared subscriptions (`$share/<group>/<filter>`)
- `priority` user property (artmq priority queue) and Message Expiry Interval
- Keepalive ping loop
- Optional TLS (`SetTLSConfig`)
- `OnConnectionLost` callback

## Connection options

| Option | Default | Notes |
| --- | --- | --- |
| `BrokerAddr` | `localhost:1883` | host:port |
| `ClientID` | empty | broker assigns one if empty |
| `KeepAlive` | 30s | 0 disables ping loop |
| `CleanStart` | true | drop prior session state |
| `ConnectTimeout` | 10s | for the TCP+CONNACK round-trip |
| `WriteTimeout` | 10s | per outbound packet |
| `TLSConfig` | nil | non-nil enables TLS |
| `MaxInflight` | 1024 | cap on concurrent unacked QoS 1/2 publishes; further Publish calls block until a slot frees |

## Publish options

| Helper | Effect |
| --- | --- |
| `WithQoS(0/1/2)` | delivery guarantee |
| `WithRetain(true)` | MQTT retain flag |
| `WithPriority(n)` | sets the `priority` user property; artmq treats lower values as higher priority (Unix `nice`-style) |
| `WithTTL(d)` | MQTT 5 Message Expiry Interval |
| `WithContentType(s)` | MQTT 5 Content Type property |

## Examples

All examples assume the artmq broker is listening on `localhost:1883`.

| Path | What it shows |
| --- | --- |
| [`examples/pubsub`](./examples/pubsub) | end-to-end: connect, subscribe, publish at QoS 0/1/2 |
| [`examples/qos`](./examples/qos) | per-QoS publish behaviour and what the client awaits internally (PUBACK / PUBREC+PUBREL+PUBCOMP) |
| [`examples/wildcards`](./examples/wildcards) | `+` (single-level) and `#` (multi-level) topic filters; how the topic matcher routes to multiple handlers |
| [`examples/priority`](./examples/priority) | artmq priority queue via `WithPriority` (lower value served first) plus `WithTTL` |
| [`examples/shared-subs`](./examples/shared-subs) | MQTT 5 `$share/<group>/<filter>` for load-balanced worker pools |
| [`examples/bench`](./examples/bench) | local sanity-check publisher harness; flags for QoS / concurrency / payload size; reports msg/s and p50/p95/p99 latency |

```bash
go run ./examples/pubsub
go run ./examples/qos
go run ./examples/wildcards
go run ./examples/priority
go run ./examples/shared-subs
```

## Testing

```bash
go test ./...
```

Unit tests cover packet encoding/decoding, the topic matcher, and the
two CONNACK layouts the SDK accepts (spec-compliant and the artmq server's
truncated form).
