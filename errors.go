package artmq

import "errors"

var (
	ErrNotConnected      = errors.New("artmq: client is not connected")
	ErrAlreadyConnected  = errors.New("artmq: client is already connected")
	ErrConnectTimeout    = errors.New("artmq: connect timed out")
	ErrPacketIDExhausted = errors.New("artmq: packet ID space exhausted")
)
