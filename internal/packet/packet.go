package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

var (
	ErrTooShort      = errors.New("packet too short for IPv4 header")
	ErrNotIPv4       = errors.New("not an IPv4 packet")
	ErrInvalidLength = errors.New("invalid IPv4 header length")
	ErrTruncated     = errors.New("packet truncated (total length mismatch)")
)

// Packet represents a parsed IPv4 packet header.
type Packet struct {
	Version  uint8
	IHL      uint8
	TotalLen uint16
	Protocol uint8
	SrcAddr  netip.Addr
	DstAddr  netip.Addr
	Raw      []byte
}

// ParseIPv4 parses raw bytes as an IPv4 packet.
func ParseIPv4(data []byte) (*Packet, error) {
	if len(data) < 20 {
		return nil, ErrTooShort
	}

	version := data[0] >> 4
	if version != 4 {
		return nil, fmt.Errorf("%w: version %d", ErrNotIPv4, version)
	}

	ihl := data[0] & 0x0F
	if ihl < 5 {
		return nil, fmt.Errorf("%w: IHL %d", ErrInvalidLength, ihl)
	}

	headerLen := int(ihl) * 4
	if len(data) < headerLen {
		return nil, ErrTooShort
	}

	totalLen := binary.BigEndian.Uint16(data[2:4])
	if totalLen == 0 {
		return nil, fmt.Errorf("%w: total_len=0", ErrInvalidLength)
	}
	if int(totalLen) < headerLen {
		return nil, fmt.Errorf("%w: total_len=%d header_len=%d", ErrInvalidLength, totalLen, headerLen)
	}
	if int(totalLen) > len(data) {
		return nil, fmt.Errorf("%w: total_len=%d actual=%d", ErrTruncated, totalLen, len(data))
	}

	protocol := data[9]
	srcAddr := netip.AddrFrom4([4]byte(data[12:16]))
	dstAddr := netip.AddrFrom4([4]byte(data[16:20]))

	return &Packet{
		Version:  version,
		IHL:      ihl,
		TotalLen: totalLen,
		Protocol: protocol,
		SrcAddr:  srcAddr,
		DstAddr:  dstAddr,
		Raw:      data[:totalLen],
	}, nil
}
