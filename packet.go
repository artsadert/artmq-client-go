package artmq

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MQTT 5 control packet types.
const (
	pktCONNECT     = 1
	pktCONNACK     = 2
	pktPUBLISH     = 3
	pktPUBACK      = 4
	pktPUBREC      = 5
	pktPUBREL      = 6
	pktPUBCOMP     = 7
	pktSUBSCRIBE   = 8
	pktSUBACK      = 9
	pktUNSUBSCRIBE = 10
	pktUNSUBACK    = 11
	pktPINGREQ     = 12
	pktPINGRESP    = 13
	pktDISCONNECT  = 14
)

// QoS levels.
const (
	QoS0 byte = 0
	QoS1 byte = 1
	QoS2 byte = 2
)

// MQTT 5 PUBLISH property identifiers used by this SDK.
const (
	propPayloadFormatIndicator    byte = 0x01
	propMessageExpiryInterval     byte = 0x02
	propContentType               byte = 0x03
	propResponseTopic             byte = 0x08
	propCorrelationData           byte = 0x09
	propSubscriptionIdentifier    byte = 0x0B
	propTopicAlias                byte = 0x23
	propUserProperty              byte = 0x26
)

// rawPacket is a parsed MQTT control packet.
type rawPacket struct {
	pktType byte
	flags   byte
	body    []byte
}

func encodeVarInt(value int) []byte {
	if value == 0 {
		return []byte{0x00}
	}
	out := make([]byte, 0, 4)
	for value > 0 {
		digit := byte(value % 128)
		value /= 128
		if value > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
	}
	return out
}

func decodeVarInt(r *bufio.Reader) (int, int, error) {
	multiplier := 1
	value := 0
	consumed := 0
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, consumed, err
		}
		consumed++
		value += int(b&0x7F) * multiplier
		if b&0x80 == 0 {
			return value, consumed, nil
		}
		multiplier *= 128
	}
	return 0, consumed, errors.New("malformed variable byte integer")
}

func writeUTF8(buf *[]byte, s string) {
	*buf = append(*buf, byte(len(s)>>8), byte(len(s)&0xFF))
	*buf = append(*buf, []byte(s)...)
}

func readUTF8(r *bufio.Reader) (string, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readUint16(r *bufio.Reader) (uint16, error) {
	var v uint16
	err := binary.Read(r, binary.BigEndian, &v)
	return v, err
}

// readPacket parses a single MQTT control packet from r.
func readPacket(r *bufio.Reader) (*rawPacket, error) {
	first, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	remaining, _, err := decodeVarInt(r)
	if err != nil {
		return nil, err
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return &rawPacket{
		pktType: first >> 4,
		flags:   first & 0x0F,
		body:    body,
	}, nil
}

// buildPacket prepends the fixed header to body.
func buildPacket(pktType, flags byte, body []byte) []byte {
	first := (pktType << 4) | (flags & 0x0F)
	rem := encodeVarInt(len(body))
	out := make([]byte, 0, 1+len(rem)+len(body))
	out = append(out, first)
	out = append(out, rem...)
	out = append(out, body...)
	return out
}

// CONNECT.
type connectPacket struct {
	ClientID  string
	KeepAlive uint16
	CleanStart bool
	Username  string
	Password  string
}

func (c *connectPacket) encode() []byte {
	var vh []byte
	writeUTF8(&vh, "MQTT")
	vh = append(vh, 5) // protocol level

	var flags byte
	if c.CleanStart {
		flags |= 0x02
	}
	if c.Username != "" {
		flags |= 0x80
	}
	if c.Password != "" {
		flags |= 0x40
	}
	vh = append(vh, flags)

	vh = append(vh, byte(c.KeepAlive>>8), byte(c.KeepAlive&0xFF))

	// MQTT 5: properties (empty).
	vh = append(vh, 0x00)

	// Payload.
	var payload []byte
	writeUTF8(&payload, c.ClientID)
	if c.Username != "" {
		writeUTF8(&payload, c.Username)
	}
	if c.Password != "" {
		writeUTF8(&payload, c.Password)
	}

	body := append(vh, payload...)
	return buildPacket(pktCONNECT, 0, body)
}

// CONNACK reason code interpretation. The artmq server sends a 2-byte body
// of [reason_code, 0x00]. A spec-compliant CONNACK is
// [ack_flags, reason_code, prop_len...]. We accept either: success requires
// the reason code at byte 1 to be 0; if that fails we also accept byte 0 == 0
// to interoperate with the current artmq server build.
func parseConnack(body []byte) (reasonCode byte, err error) {
	if len(body) < 2 {
		return 0xFF, errors.New("malformed CONNACK")
	}
	// Spec position.
	if body[1] == 0x00 {
		return 0x00, nil
	}
	// Lenient fallback: server placed reason code at byte 0.
	if body[0] != 0x00 {
		return body[0], fmt.Errorf("connect refused, reason code 0x%02x", body[0])
	}
	return body[1], fmt.Errorf("connect refused, reason code 0x%02x", body[1])
}

// PUBLISH outgoing.
type publishPacket struct {
	Topic                 string
	PacketID              uint16
	QoS                   byte
	Retain                bool
	Payload               []byte
	MessageExpiryInterval *uint32 // seconds; nil = unset
	Priority              *int64  // user property "priority"; nil = unset
	ContentType           string  // empty = unset
}

func (p *publishPacket) encode() []byte {
	flags := (p.QoS & 0x03) << 1
	if p.Retain {
		flags |= 0x01
	}

	var vh []byte
	writeUTF8(&vh, p.Topic)
	if p.QoS > 0 {
		vh = append(vh, byte(p.PacketID>>8), byte(p.PacketID&0xFF))
	}

	// Properties.
	var props []byte
	if p.MessageExpiryInterval != nil {
		props = append(props, propMessageExpiryInterval)
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], *p.MessageExpiryInterval)
		props = append(props, b[:]...)
	}
	if p.ContentType != "" {
		props = append(props, propContentType)
		writeUTF8(&props, p.ContentType)
	}
	if p.Priority != nil {
		props = append(props, propUserProperty)
		writeUTF8(&props, "priority")
		writeUTF8(&props, fmt.Sprintf("%d", *p.Priority))
	}
	vh = append(vh, encodeVarInt(len(props))...)
	vh = append(vh, props...)

	body := append(vh, p.Payload...)
	return buildPacket(pktPUBLISH, flags, body)
}

// decodePublish parses an inbound PUBLISH from a parsed rawPacket.
func decodePublish(pkt *rawPacket) (topic string, packetID uint16, qos byte, payload []byte, err error) {
	qos = (pkt.flags >> 1) & 0x03
	r := bufio.NewReader(bytes.NewReader(pkt.body))

	topic, err = readUTF8(r)
	if err != nil {
		return "", 0, 0, nil, err
	}
	if qos > 0 {
		packetID, err = readUint16(r)
		if err != nil {
			return "", 0, 0, nil, err
		}
	}
	propLen, _, err := decodeVarInt(r)
	if err != nil {
		return "", 0, 0, nil, err
	}
	if propLen > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(propLen)); err != nil {
			return "", 0, 0, nil, err
		}
	}
	payload, err = io.ReadAll(r)
	if err != nil {
		return "", 0, 0, nil, err
	}
	return topic, packetID, qos, payload, nil
}

// SUBSCRIBE.
type subscribePacket struct {
	PacketID uint16
	Filters  []subFilter
}

type subFilter struct {
	Topic string
	QoS   byte
}

func (s *subscribePacket) encode() []byte {
	var body []byte
	body = append(body, byte(s.PacketID>>8), byte(s.PacketID&0xFF))
	body = append(body, 0x00) // properties length = 0
	for _, f := range s.Filters {
		writeUTF8(&body, f.Topic)
		body = append(body, f.QoS&0x03)
	}
	// Reserved bits 0010 per spec for SUBSCRIBE.
	return buildPacket(pktSUBSCRIBE, 0x02, body)
}

// SUBACK parsing: [packetID(2)] [propLen varint] [reasonCode...]
func parseSuback(body []byte) (packetID uint16, reasonCodes []byte, err error) {
	if len(body) < 2 {
		return 0, nil, errors.New("malformed SUBACK")
	}
	r := bufio.NewReader(bytes.NewReader(body))
	packetID, err = readUint16(r)
	if err != nil {
		return 0, nil, err
	}
	propLen, _, err := decodeVarInt(r)
	if err != nil {
		return 0, nil, err
	}
	if propLen > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(propLen)); err != nil {
			return 0, nil, err
		}
	}
	reasonCodes, err = io.ReadAll(r)
	if err != nil {
		return 0, nil, err
	}
	return packetID, reasonCodes, nil
}

// Ack packets (PUBACK/PUBREC/PUBREL/PUBCOMP).
func encodeAck(pktType byte, packetID uint16) []byte {
	flags := byte(0)
	if pktType == pktPUBREL {
		flags = 0x02
	}
	body := []byte{
		byte(packetID >> 8), byte(packetID & 0xFF),
		0x00, // reason code = success
		0x00, // property length = 0
	}
	return buildPacket(pktType, flags, body)
}

func parseAck(body []byte) (packetID uint16, reasonCode byte, err error) {
	if len(body) < 2 {
		return 0, 0xFF, errors.New("malformed ack packet")
	}
	packetID = uint16(body[0])<<8 | uint16(body[1])
	if len(body) >= 3 {
		reasonCode = body[2]
	}
	return packetID, reasonCode, nil
}

func encodePingReq() []byte {
	return []byte{byte(pktPINGREQ << 4), 0x00}
}

func encodeDisconnect() []byte {
	return []byte{byte(pktDISCONNECT << 4), 0x00}
}
