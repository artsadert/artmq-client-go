// Package artmq is a Go SDK for the artmq message broker. The broker speaks
// MQTT 5.0 over TCP on port 1883 by default; this client wraps that protocol
// in a Paho-style API: Connect, Publish, Subscribe, Disconnect.
//
// Example:
//
//	opts := artmq.NewClientOptions().
//	    SetBrokerAddr("localhost:1883").
//	    SetClientID("svc-1")
//	c := artmq.NewClient(opts)
//	if err := c.Connect(ctx); err != nil { ... }
//	defer c.Disconnect()
//
//	c.Subscribe(ctx, "events/#", artmq.QoS1, func(topic string, payload []byte) {
//	    fmt.Println(topic, string(payload))
//	})
//	c.Publish(ctx, "events/foo", []byte("hi"), artmq.WithQoS(1))
package artmq

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// MessageHandler is invoked for every message delivered to a subscription.
// It is called from the client's read goroutine; long-running work should be
// dispatched to another goroutine to avoid blocking message delivery.
type MessageHandler func(topic string, payload []byte)

// Client is a connection to an artmq broker. It is safe for concurrent use
// after Connect returns.
type Client struct {
	opts *ClientOptions

	mu        sync.Mutex
	conn      net.Conn
	writer    *bufio.Writer
	reader    *bufio.Reader
	connected atomic.Bool
	closing   atomic.Bool

	writeMu sync.Mutex // serializes writes to conn

	// Packet ID allocator (1..65535).
	pidMu      sync.Mutex
	nextPID    uint16
	pubAcks    map[uint16]chan byte // PUBACK reason codes by packet id (QoS 1)
	pubRecs    map[uint16]chan byte // PUBREC reason codes by packet id (QoS 2)
	pubComps   map[uint16]chan byte // PUBCOMP reason codes by packet id (QoS 2)
	subAcks    map[uint16]chan []byte

	// Subscriptions: filter -> handler. Routed locally for inbound PUBLISH.
	subsMu   sync.RWMutex
	handlers map[string]MessageHandler

	stopPing chan struct{}
	doneCh   chan struct{}

	// Last error from read loop.
	lastErr atomic.Value // error
}

// NewClient builds an unconnected Client.
func NewClient(opts *ClientOptions) *Client {
	if opts == nil {
		opts = NewClientOptions()
	}
	return &Client{
		opts:     opts,
		nextPID:  1,
		pubAcks:  make(map[uint16]chan byte),
		pubRecs:  make(map[uint16]chan byte),
		pubComps: make(map[uint16]chan byte),
		subAcks:  make(map[uint16]chan []byte),
		handlers: make(map[string]MessageHandler),
	}
}

// IsConnected reports whether the client currently has an active session.
func (c *Client) IsConnected() bool { return c.connected.Load() }

// Connect dials the broker and performs the MQTT 5 handshake.
func (c *Client) Connect(ctx context.Context) error {
	if err := c.opts.validate(); err != nil {
		return err
	}
	if c.connected.Load() {
		return ErrAlreadyConnected
	}
	if err := c.dialAndHandshake(ctx); err != nil {
		return err
	}
	c.startBackgroundLoops()
	return nil
}

func (c *Client) dialAndHandshake(ctx context.Context) error {
	dialCtx := ctx
	if c.opts.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, c.opts.ConnectTimeout)
		defer cancel()
	}

	d := net.Dialer{}
	rawConn, err := d.DialContext(dialCtx, "tcp", c.opts.BrokerAddr)
	if err != nil {
		return fmt.Errorf("artmq: dial: %w", err)
	}

	var conn net.Conn = rawConn
	if c.opts.TLSConfig != nil {
		tlsConn := tls.Client(rawConn, c.opts.TLSConfig)
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			rawConn.Close()
			return fmt.Errorf("artmq: tls handshake: %w", err)
		}
		conn = tlsConn
	}

	c.mu.Lock()
	c.conn = conn
	c.writer = bufio.NewWriter(conn)
	c.reader = bufio.NewReader(conn)
	c.mu.Unlock()

	keepAliveSecs := uint16(c.opts.KeepAlive / time.Second)
	connect := &connectPacket{
		ClientID:   c.opts.ClientID,
		KeepAlive:  keepAliveSecs,
		CleanStart: c.opts.CleanStart,
		Username:   c.opts.Username,
		Password:   c.opts.Password,
	}

	// Set a read deadline for the CONNACK only.
	if dl, ok := dialCtx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
		_ = conn.SetWriteDeadline(dl)
	}

	if err := c.writeRaw(connect.encode()); err != nil {
		conn.Close()
		return fmt.Errorf("artmq: write CONNECT: %w", err)
	}

	pkt, err := readPacket(c.reader)
	if err != nil {
		conn.Close()
		return fmt.Errorf("artmq: read CONNACK: %w", err)
	}
	if pkt.pktType != pktCONNACK {
		conn.Close()
		return fmt.Errorf("artmq: expected CONNACK, got packet type %d", pkt.pktType)
	}
	if _, err := parseConnack(pkt.body); err != nil {
		conn.Close()
		return err
	}

	// Clear deadlines for the steady-state read loop.
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	c.connected.Store(true)
	c.closing.Store(false)
	c.stopPing = make(chan struct{})
	c.doneCh = make(chan struct{})
	return nil
}

func (c *Client) startBackgroundLoops() {
	go c.readLoop()
	if c.opts.KeepAlive > 0 {
		go c.pingLoop()
	}
}

// Disconnect sends a DISCONNECT packet and closes the connection. Safe to
// call multiple times.
func (c *Client) Disconnect() error {
	if !c.connected.Load() {
		return nil
	}
	c.closing.Store(true)
	_ = c.writeRaw(encodeDisconnect())
	c.shutdown(nil)
	return nil
}

func (c *Client) shutdown(cause error) {
	if !c.connected.CompareAndSwap(true, false) {
		return
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}

	// Unblock any waiters with a connection-error reason.
	c.pidMu.Lock()
	for id, ch := range c.pubAcks {
		close(ch)
		delete(c.pubAcks, id)
	}
	for id, ch := range c.pubRecs {
		close(ch)
		delete(c.pubRecs, id)
	}
	for id, ch := range c.pubComps {
		close(ch)
		delete(c.pubComps, id)
	}
	for id, ch := range c.subAcks {
		close(ch)
		delete(c.subAcks, id)
	}
	c.pidMu.Unlock()

	if c.stopPing != nil {
		select {
		case <-c.stopPing:
		default:
			close(c.stopPing)
		}
	}
	if c.doneCh != nil {
		select {
		case <-c.doneCh:
		default:
			close(c.doneCh)
		}
	}

	if cause != nil {
		c.lastErr.Store(cause)
		if c.opts.OnConnectionLost != nil {
			go c.opts.OnConnectionLost(cause)
		}
	}
}

func (c *Client) writeRaw(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	if c.opts.WriteTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
		defer conn.SetWriteDeadline(time.Time{})
	}
	if _, err := conn.Write(data); err != nil {
		return err
	}
	return nil
}

// allocatePacketID reserves the next available 1..65535 packet id.
func (c *Client) allocatePacketID() (uint16, error) {
	c.pidMu.Lock()
	defer c.pidMu.Unlock()
	for i := 0; i < 65535; i++ {
		id := c.nextPID
		c.nextPID++
		if c.nextPID == 0 {
			c.nextPID = 1
		}
		if _, busy := c.pubAcks[id]; busy {
			continue
		}
		if _, busy := c.pubRecs[id]; busy {
			continue
		}
		if _, busy := c.subAcks[id]; busy {
			continue
		}
		return id, nil
	}
	return 0, ErrPacketIDExhausted
}

// Publish sends a message to topic. For QoS 1/2, Publish blocks until the
// broker acknowledges (PUBACK / PUBCOMP) or ctx is canceled.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, opts ...PublishOption) error {
	if !c.connected.Load() {
		return ErrNotConnected
	}
	pkt := &publishPacket{Topic: topic, Payload: payload}
	for _, o := range opts {
		o(pkt)
	}

	switch pkt.QoS {
	case 0:
		return c.writeRaw(pkt.encode())
	case 1:
		id, err := c.allocatePacketID()
		if err != nil {
			return err
		}
		pkt.PacketID = id
		ackCh := make(chan byte, 1)
		c.pidMu.Lock()
		c.pubAcks[id] = ackCh
		c.pidMu.Unlock()

		if err := c.writeRaw(pkt.encode()); err != nil {
			c.pidMu.Lock()
			delete(c.pubAcks, id)
			c.pidMu.Unlock()
			return err
		}
		select {
		case rc, ok := <-ackCh:
			if !ok {
				return ErrNotConnected
			}
			if rc >= 0x80 {
				return fmt.Errorf("artmq: PUBACK reason 0x%02x", rc)
			}
			return nil
		case <-ctx.Done():
			c.pidMu.Lock()
			delete(c.pubAcks, id)
			c.pidMu.Unlock()
			return ctx.Err()
		}
	case 2:
		id, err := c.allocatePacketID()
		if err != nil {
			return err
		}
		pkt.PacketID = id
		recCh := make(chan byte, 1)
		compCh := make(chan byte, 1)
		c.pidMu.Lock()
		c.pubRecs[id] = recCh
		c.pubComps[id] = compCh
		c.pidMu.Unlock()
		cleanup := func() {
			c.pidMu.Lock()
			delete(c.pubRecs, id)
			delete(c.pubComps, id)
			c.pidMu.Unlock()
		}
		if err := c.writeRaw(pkt.encode()); err != nil {
			cleanup()
			return err
		}
		select {
		case rc, ok := <-recCh:
			if !ok {
				cleanup()
				return ErrNotConnected
			}
			if rc >= 0x80 {
				cleanup()
				return fmt.Errorf("artmq: PUBREC reason 0x%02x", rc)
			}
		case <-ctx.Done():
			cleanup()
			return ctx.Err()
		}
		// Send PUBREL, wait for PUBCOMP.
		if err := c.writeRaw(encodeAck(pktPUBREL, id)); err != nil {
			cleanup()
			return err
		}
		select {
		case rc, ok := <-compCh:
			cleanup()
			if !ok {
				return ErrNotConnected
			}
			if rc >= 0x80 {
				return fmt.Errorf("artmq: PUBCOMP reason 0x%02x", rc)
			}
			return nil
		case <-ctx.Done():
			cleanup()
			return ctx.Err()
		}
	default:
		return fmt.Errorf("artmq: invalid QoS %d", pkt.QoS)
	}
}

// Subscribe registers handler for messages matching filter and sends a
// SUBSCRIBE packet to the broker. The MQTT 5 shared-subscription syntax
// "$share/<group>/<filter>" is supported by the artmq broker.
func (c *Client) Subscribe(ctx context.Context, filter string, qos byte, handler MessageHandler) error {
	if !c.connected.Load() {
		return ErrNotConnected
	}
	if handler == nil {
		return errors.New("artmq: handler is required")
	}

	id, err := c.allocatePacketID()
	if err != nil {
		return err
	}
	ackCh := make(chan []byte, 1)
	c.pidMu.Lock()
	c.subAcks[id] = ackCh
	c.pidMu.Unlock()

	pkt := &subscribePacket{
		PacketID: id,
		Filters:  []subFilter{{Topic: storedFilter(filter), QoS: qos}},
	}
	// Register handler under the *unwrapped* topic so router matches inbound
	// PUBLISH topics (which never carry the $share/ prefix).
	c.subsMu.Lock()
	c.handlers[storedFilter(filter)] = handler
	c.subsMu.Unlock()

	// But on the wire we send the original filter (possibly prefixed with $share/).
	pkt.Filters[0].Topic = filter

	if err := c.writeRaw(pkt.encode()); err != nil {
		c.pidMu.Lock()
		delete(c.subAcks, id)
		c.pidMu.Unlock()
		c.subsMu.Lock()
		delete(c.handlers, storedFilter(filter))
		c.subsMu.Unlock()
		return err
	}

	select {
	case rcs, ok := <-ackCh:
		if !ok {
			return ErrNotConnected
		}
		for _, rc := range rcs {
			if rc >= 0x80 {
				c.subsMu.Lock()
				delete(c.handlers, storedFilter(filter))
				c.subsMu.Unlock()
				return fmt.Errorf("artmq: SUBACK reason 0x%02x", rc)
			}
		}
		return nil
	case <-ctx.Done():
		c.pidMu.Lock()
		delete(c.subAcks, id)
		c.pidMu.Unlock()
		return ctx.Err()
	}
}

// storedFilter strips the $share/<group>/ prefix so the local matcher uses
// the inner filter (which is what published topics will look like on the
// wire).
func storedFilter(raw string) string {
	const prefix = "$share/"
	if len(raw) <= len(prefix) || raw[:len(prefix)] != prefix {
		return raw
	}
	rest := raw[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[i+1:]
		}
	}
	return raw
}

func (c *Client) readLoop() {
	for {
		pkt, err := readPacket(c.reader)
		if err != nil {
			if c.closing.Load() {
				c.shutdown(nil)
				return
			}
			if errors.Is(err, io.EOF) {
				c.shutdown(io.EOF)
				return
			}
			c.shutdown(err)
			return
		}
		c.handlePacket(pkt)
	}
}

func (c *Client) handlePacket(pkt *rawPacket) {
	switch pkt.pktType {
	case pktPUBLISH:
		topic, packetID, qos, payload, err := decodePublish(pkt)
		if err != nil {
			return
		}
		c.dispatchInbound(topic, payload)
		switch qos {
		case 1:
			_ = c.writeRaw(encodeAck(pktPUBACK, packetID))
		case 2:
			_ = c.writeRaw(encodeAck(pktPUBREC, packetID))
		}
	case pktPUBACK:
		id, rc, err := parseAck(pkt.body)
		if err != nil {
			return
		}
		c.pidMu.Lock()
		ch, ok := c.pubAcks[id]
		delete(c.pubAcks, id)
		c.pidMu.Unlock()
		if ok {
			ch <- rc
		}
	case pktPUBREC:
		id, rc, err := parseAck(pkt.body)
		if err != nil {
			return
		}
		c.pidMu.Lock()
		ch, ok := c.pubRecs[id]
		delete(c.pubRecs, id)
		c.pidMu.Unlock()
		if ok {
			ch <- rc
		}
	case pktPUBREL:
		id, _, err := parseAck(pkt.body)
		if err != nil {
			return
		}
		_ = c.writeRaw(encodeAck(pktPUBCOMP, id))
	case pktPUBCOMP:
		id, rc, err := parseAck(pkt.body)
		if err != nil {
			return
		}
		c.pidMu.Lock()
		ch, ok := c.pubComps[id]
		delete(c.pubComps, id)
		c.pidMu.Unlock()
		if ok {
			ch <- rc
		}
	case pktSUBACK:
		id, rcs, err := parseSuback(pkt.body)
		if err != nil {
			return
		}
		c.pidMu.Lock()
		ch, ok := c.subAcks[id]
		delete(c.subAcks, id)
		c.pidMu.Unlock()
		if ok {
			ch <- rcs
		}
	case pktPINGRESP:
		// no-op
	case pktDISCONNECT:
		c.closing.Store(true)
		c.shutdown(errors.New("artmq: server-initiated disconnect"))
	}
}

func (c *Client) dispatchInbound(topic string, payload []byte) {
	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	for filter, handler := range c.handlers {
		if MatchTopic(filter, topic) {
			handler(topic, payload)
		}
	}
}

func (c *Client) pingLoop() {
	interval := c.opts.KeepAlive / 2
	if interval < time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if !c.connected.Load() {
				return
			}
			if err := c.writeRaw(encodePingReq()); err != nil {
				return
			}
		case <-c.stopPing:
			return
		}
	}
}
