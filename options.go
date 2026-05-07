package artmq

import (
	"crypto/tls"
	"fmt"
	"time"
)

// ClientOptions configures Client behavior. Use NewClientOptions and the
// chained setters; zero values are not safe.
type ClientOptions struct {
	BrokerAddr        string // host:port, e.g. "localhost:1883"
	ClientID          string // empty -> server assigns
	Username          string
	Password          string
	KeepAlive         time.Duration // 0 disables keepalive pings
	CleanStart        bool
	ConnectTimeout    time.Duration
	WriteTimeout      time.Duration
	AutoReconnect     bool
	ReconnectDelay    time.Duration // initial delay; doubles up to MaxReconnectDelay
	MaxReconnectDelay time.Duration
	TLSConfig         *tls.Config // nil -> plain TCP
	MaxInflight       int         // max concurrent unacked QoS>=1 publishes; 0 -> default

	// OnConnectionLost is invoked when the read loop terminates with an error.
	// If AutoReconnect is true, the client will attempt to reconnect after.
	OnConnectionLost func(err error)
}

// NewClientOptions returns options with sensible defaults.
func NewClientOptions() *ClientOptions {
	return &ClientOptions{
		BrokerAddr:        "localhost:1883",
		KeepAlive:         30 * time.Second,
		CleanStart:        true,
		ConnectTimeout:    10 * time.Second,
		WriteTimeout:      10 * time.Second,
		ReconnectDelay:    1 * time.Second,
		MaxReconnectDelay: 30 * time.Second,
		MaxInflight:       1024,
	}
}

func (o *ClientOptions) SetBrokerAddr(addr string) *ClientOptions    { o.BrokerAddr = addr; return o }
func (o *ClientOptions) SetClientID(id string) *ClientOptions        { o.ClientID = id; return o }
func (o *ClientOptions) SetUsername(u string) *ClientOptions         { o.Username = u; return o }
func (o *ClientOptions) SetPassword(p string) *ClientOptions         { o.Password = p; return o }
func (o *ClientOptions) SetKeepAlive(d time.Duration) *ClientOptions { o.KeepAlive = d; return o }
func (o *ClientOptions) SetCleanStart(v bool) *ClientOptions         { o.CleanStart = v; return o }
func (o *ClientOptions) SetConnectTimeout(d time.Duration) *ClientOptions {
	o.ConnectTimeout = d
	return o
}
func (o *ClientOptions) SetAutoReconnect(v bool) *ClientOptions    { o.AutoReconnect = v; return o }
func (o *ClientOptions) SetTLSConfig(c *tls.Config) *ClientOptions { o.TLSConfig = c; return o }

// SetMaxInflight bounds the number of concurrently-unacknowledged QoS 1 / 2
// publishes. When the cap is reached, additional Publish calls block until
// an ack frees a slot (or ctx is canceled). QoS 0 is not counted because it
// has no ack.
func (o *ClientOptions) SetMaxInflight(n int) *ClientOptions {
	if n < 1 {
		n = 1
	}
	o.MaxInflight = n
	return o
}
func (o *ClientOptions) SetOnConnectionLost(fn func(error)) *ClientOptions {
	o.OnConnectionLost = fn
	return o
}

func (o *ClientOptions) validate() error {
	if o.BrokerAddr == "" {
		return fmt.Errorf("artmq: BrokerAddr is required")
	}
	return nil
}

// PublishOption mutates a publishPacket before it is sent.
type PublishOption func(*publishPacket)

// WithQoS sets the QoS level for the published message.
func WithQoS(q byte) PublishOption {
	return func(p *publishPacket) {
		if q > 2 {
			q = 2
		}
		p.QoS = q
	}
}

// WithRetain sets the MQTT retain flag.
func WithRetain(v bool) PublishOption {
	return func(p *publishPacket) { p.Retain = v }
}

// WithPriority attaches a "priority" user property; higher values are
// dispatched earlier by the artmq broker.
func WithPriority(prio int64) PublishOption {
	return func(p *publishPacket) { v := prio; p.Priority = &v }
}

// WithTTL sets the MQTT 5 Message Expiry Interval. The artmq broker drops
// messages that exceed this lifetime before delivery.
func WithTTL(d time.Duration) PublishOption {
	return func(p *publishPacket) {
		secs := uint32(d / time.Second)
		p.MessageExpiryInterval = &secs
	}
}

// WithContentType sets the MQTT 5 Content Type property.
func WithContentType(ct string) PublishOption {
	return func(p *publishPacket) { p.ContentType = ct }
}
