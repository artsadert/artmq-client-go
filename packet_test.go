package artmq

import (
	"bufio"
	"bytes"
	"testing"
)

func TestVarIntRoundTrip(t *testing.T) {
	cases := []int{0, 1, 127, 128, 16383, 16384, 2097151, 2097152, 268435455}
	for _, n := range cases {
		buf := encodeVarInt(n)
		got, _, err := decodeVarInt(bufio.NewReader(bytes.NewReader(buf)))
		if err != nil {
			t.Fatalf("decode(%d) err: %v", n, err)
		}
		if got != n {
			t.Fatalf("varint %d: got %d", n, got)
		}
	}
}

func TestConnectEncode(t *testing.T) {
	p := &connectPacket{ClientID: "abc", KeepAlive: 30, CleanStart: true}
	b := p.encode()
	// Fixed header type=CONNECT, flags=0
	if b[0] != 0x10 {
		t.Fatalf("first byte = 0x%02x", b[0])
	}
	// Variable header should start with: 0x00 0x04 'M' 'Q' 'T' 'T' 0x05
	if !bytes.Equal(b[2:9], []byte{0x00, 0x04, 'M', 'Q', 'T', 'T', 0x05}) {
		t.Fatalf("bad protocol header: % x", b[2:9])
	}
	// Connect flags byte: cleanStart=1 -> 0x02
	if b[9] != 0x02 {
		t.Fatalf("connect flags = 0x%02x", b[9])
	}
	// Keepalive = 30 -> 0x00 0x1e
	if b[10] != 0x00 || b[11] != 0x1e {
		t.Fatalf("keepalive bytes = % x", b[10:12])
	}
	// Properties length = 0
	if b[12] != 0x00 {
		t.Fatalf("properties length = 0x%02x", b[12])
	}
	// Client ID length(2) + bytes
	if b[13] != 0x00 || b[14] != 0x03 {
		t.Fatalf("client id len bytes = % x", b[13:15])
	}
	if string(b[15:18]) != "abc" {
		t.Fatalf("client id = %q", b[15:18])
	}
}

func TestPublishEncodeQoS0(t *testing.T) {
	p := &publishPacket{Topic: "t", Payload: []byte("hello"), QoS: 0}
	b := p.encode()
	if b[0]&0xF0 != 0x30 {
		t.Fatalf("type bits = 0x%02x", b[0])
	}
	// remaining length = topiclen(2)+topic(1)+propLen(1)+payload(5) = 9
	if b[1] != 9 {
		t.Fatalf("rem length = %d", b[1])
	}
	body := b[2:]
	// topic-length/topic
	if body[0] != 0x00 || body[1] != 0x01 || body[2] != 't' {
		t.Fatalf("topic encoding: % x", body[:3])
	}
	if body[3] != 0x00 {
		t.Fatalf("propLen = 0x%02x", body[3])
	}
	if string(body[4:]) != "hello" {
		t.Fatalf("payload = %q", body[4:])
	}
}

func TestPublishEncodePriorityProperty(t *testing.T) {
	prio := int64(7)
	p := &publishPacket{Topic: "t", QoS: 1, PacketID: 5, Payload: []byte("x"), Priority: &prio}
	b := p.encode()
	// Decode back via decodePublish to make sure topic/payload survive.
	pkt, err := readPacket(bufio.NewReader(bytes.NewReader(b)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	topic, id, qos, payload, err := decodePublish(pkt)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if topic != "t" || id != 5 || qos != 1 || string(payload) != "x" {
		t.Fatalf("got %q/%d/%d/%q", topic, id, qos, payload)
	}
}

func TestSubscribeEncodeReservedBits(t *testing.T) {
	p := &subscribePacket{PacketID: 1, Filters: []subFilter{{Topic: "a/+", QoS: 1}}}
	b := p.encode()
	// SUBSCRIBE first byte must be 0x82 (type=8 << 4 | reserved=0b0010).
	if b[0] != 0x82 {
		t.Fatalf("first byte = 0x%02x", b[0])
	}
}

func TestParseSubackHappy(t *testing.T) {
	// packetID=2, propLen=0, reasonCodes=[0x01]
	body := []byte{0x00, 0x02, 0x00, 0x01}
	id, rcs, err := parseSuback(body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if id != 2 {
		t.Fatalf("id=%d", id)
	}
	if len(rcs) != 1 || rcs[0] != 0x01 {
		t.Fatalf("rcs=% x", rcs)
	}
}

func TestParseConnackBothLayouts(t *testing.T) {
	// Spec layout: ack_flags=0, reason_code=0
	if rc, err := parseConnack([]byte{0x00, 0x00}); err != nil || rc != 0 {
		t.Fatalf("spec layout: rc=%v err=%v", rc, err)
	}
	// artmq server layout: [reason=0, propLen=0]
	if rc, err := parseConnack([]byte{0x00, 0x00}); err != nil || rc != 0 {
		t.Fatalf("artmq layout: rc=%v err=%v", rc, err)
	}
}

func TestMatchTopic(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"a/b", "a/b", true},
		{"a/+", "a/b", true},
		{"a/+", "a/b/c", false},
		{"a/#", "a/b/c/d", true},
		{"a/#", "a", true}, // per MQTT 5 §4.7.1.2: # matches parent + descendants
		{"a/b/+", "a/b", false},
		{"#", "anything/at/all", true},
	}
	for _, tc := range cases {
		if got := MatchTopic(tc.filter, tc.topic); got != tc.want {
			t.Errorf("MatchTopic(%q,%q)=%v want %v", tc.filter, tc.topic, got, tc.want)
		}
	}
}
