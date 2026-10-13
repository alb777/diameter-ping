// Package diameter encodes and decodes the Diameter base protocol messages dping exchanges (RFC 6733).
package diameter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
)

// Command codes (RFC 6733 section 3.1).
const (
	CapabilitiesExchange uint32 = 257
	DeviceWatchdog       uint32 = 280
	DisconnectPeer       uint32 = 282
)

// Command flags (RFC 6733 section 3).
const (
	FlagRequest   uint8 = 0x80
	FlagProxiable uint8 = 0x40
	FlagError     uint8 = 0x20
)

// AVP flags (RFC 6733 section 4.1).
const (
	FlagVendor    uint8 = 0x80
	FlagMandatory uint8 = 0x40
)

// Base protocol AVP codes (RFC 6733 section 4.5).
const (
	HostIPAddress               uint32 = 257
	AuthApplicationID           uint32 = 258
	AcctApplicationID           uint32 = 259
	VendorSpecificApplicationID uint32 = 260
	OriginHost                  uint32 = 264
	SupportedVendorID           uint32 = 265
	VendorID                    uint32 = 266
	FirmwareRevision            uint32 = 267
	ResultCode                  uint32 = 268
	ProductName                 uint32 = 269
	DisconnectCause             uint32 = 273
	OriginStateID               uint32 = 278
	FailedAVP                   uint32 = 279
	ErrorMessage                uint32 = 281
	OriginRealm                 uint32 = 296
	InbandSecurityID            uint32 = 299
)

// AVPName is the RFC 6733 name of a base protocol AVP code, or "" for any other code.
func AVPName(code uint32) string {
	return avpNames[code]
}

var avpNames = map[uint32]string{
	HostIPAddress:               "Host-IP-Address",
	AuthApplicationID:           "Auth-Application-Id",
	AcctApplicationID:           "Acct-Application-Id",
	VendorSpecificApplicationID: "Vendor-Specific-Application-Id",
	OriginHost:                  "Origin-Host",
	SupportedVendorID:           "Supported-Vendor-Id",
	VendorID:                    "Vendor-Id",
	FirmwareRevision:            "Firmware-Revision",
	ResultCode:                  "Result-Code",
	ProductName:                 "Product-Name",
	DisconnectCause:             "Disconnect-Cause",
	OriginStateID:               "Origin-State-Id",
	FailedAVP:                   "Failed-AVP",
	ErrorMessage:                "Error-Message",
	OriginRealm:                 "Origin-Realm",
	InbandSecurityID:            "Inband-Security-Id",
}

const (
	headerLen = 20
	// maxLen bounds what ReadMessage accepts: far above any capabilities exchange, far below the 24-bit limit.
	maxLen = 1 << 20
)

// AVP is one attribute-value pair. Data holds the value without its padding.
type AVP struct {
	Code     uint32
	Flags    uint8
	VendorID uint32
	Data     []byte
}

// Message is a Diameter message.
type Message struct {
	Flags    uint8
	Code     uint32
	AppID    uint32
	HopByHop uint32
	EndToEnd uint32
	AVPs     []AVP
}

// IsRequest reports whether the R bit is set.
func (m *Message) IsRequest() bool {
	return m.Flags&FlagRequest != 0
}

// Marshal encodes the message, padding every AVP to a multiple of four octets.
func (m *Message) Marshal() []byte {
	body := appendAVPs(nil, m.AVPs)
	b := make([]byte, headerLen, headerLen+len(body))
	binary.BigEndian.PutUint32(b[0:4], 1<<24|uint32(headerLen+len(body))) // version 1, message length
	binary.BigEndian.PutUint32(b[4:8], uint32(m.Flags)<<24|m.Code)
	binary.BigEndian.PutUint32(b[8:12], m.AppID)
	binary.BigEndian.PutUint32(b[12:16], m.HopByHop)
	binary.BigEndian.PutUint32(b[16:20], m.EndToEnd)
	return append(b, body...)
}

func appendAVPs(b []byte, avps []AVP) []byte {
	for _, a := range avps {
		hdr := 8
		if a.Flags&FlagVendor != 0 {
			hdr = 12
		}
		var h [12]byte
		binary.BigEndian.PutUint32(h[0:4], a.Code)
		binary.BigEndian.PutUint32(h[4:8], uint32(a.Flags)<<24|uint32(hdr+len(a.Data)))
		binary.BigEndian.PutUint32(h[8:12], a.VendorID)
		b = append(b, h[:hdr]...)
		b = append(b, a.Data...)
		b = append(b, make([]byte, pad(len(a.Data)))...)
	}
	return b
}

func pad(n int) int {
	return (4 - n%4) % 4
}

// ReadMessage reads one message from a stream such as a TCP or SCTP connection.
func ReadMessage(r io.Reader) (*Message, error) {
	b := make([]byte, headerLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	if b[0] != 1 {
		return nil, fmt.Errorf("not a Diameter message: version %d", b[0])
	}
	length := int(binary.BigEndian.Uint32(b[0:4]) & 0xffffff)
	if length < headerLen || length > maxLen || length%4 != 0 {
		return nil, fmt.Errorf("bad Diameter message length %d", length)
	}
	b = append(b, make([]byte, length-headerLen)...)
	if _, err := io.ReadFull(r, b[headerLen:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return Unmarshal(b)
}

// Unmarshal decodes one complete message.
func Unmarshal(b []byte) (*Message, error) {
	if len(b) < headerLen {
		return nil, errors.New("short Diameter header")
	}
	if length := int(binary.BigEndian.Uint32(b[0:4]) & 0xffffff); length != len(b) {
		return nil, fmt.Errorf("message length field says %d octets, got %d", length, len(b))
	}
	avps, err := ParseAVPs(b[headerLen:])
	if err != nil {
		return nil, err
	}
	return &Message{
		Flags:    b[4],
		Code:     binary.BigEndian.Uint32(b[4:8]) & 0xffffff,
		AppID:    binary.BigEndian.Uint32(b[8:12]),
		HopByHop: binary.BigEndian.Uint32(b[12:16]),
		EndToEnd: binary.BigEndian.Uint32(b[16:20]),
		AVPs:     avps,
	}, nil
}

// ParseAVPs decodes a sequence of AVPs: a message body or the value of a Grouped AVP.
func ParseAVPs(b []byte) ([]AVP, error) {
	var avps []AVP
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, errors.New("truncated AVP header")
		}
		a := AVP{Code: binary.BigEndian.Uint32(b[0:4]), Flags: b[4]}
		length := int(binary.BigEndian.Uint32(b[4:8]) & 0xffffff)
		hdr := 8
		if a.Flags&FlagVendor != 0 {
			hdr = 12
			if len(b) < hdr {
				return nil, errors.New("truncated AVP header")
			}
			a.VendorID = binary.BigEndian.Uint32(b[8:12])
		}
		if length < hdr || length > len(b) {
			return nil, fmt.Errorf("AVP %d: bad length %d", a.Code, length)
		}
		a.Data = b[hdr:length]
		avps = append(avps, a)
		// Tolerate a last AVP whose padding is missing.
		b = b[min(length+pad(length), len(b)):]
	}
	return avps, nil
}

// Find returns the first base protocol AVP (no Vendor-Id) with the given code.
func Find(avps []AVP, code uint32) (AVP, bool) {
	for _, a := range avps {
		if a.Code == code && a.Flags&FlagVendor == 0 {
			return a, true
		}
	}
	return AVP{}, false
}

// Uint32Data encodes an Unsigned32 or Enumerated value.
func Uint32Data(v uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, v)
}

// AddressData encodes an Address value: the IANA address family (1 IPv4, 2 IPv6), then the address.
func AddressData(addr netip.Addr) []byte {
	addr = addr.Unmap()
	family := uint16(1)
	if addr.Is6() {
		family = 2
	}
	return append(binary.BigEndian.AppendUint16(nil, family), addr.AsSlice()...)
}

// GroupedData encodes the value of a Grouped AVP.
func GroupedData(avps ...AVP) []byte {
	return appendAVPs(nil, avps)
}

// Uint32 decodes an Unsigned32 or Enumerated value.
func (a AVP) Uint32() (uint32, error) {
	if len(a.Data) != 4 {
		return 0, fmt.Errorf("AVP %d: %d octets, want 4", a.Code, len(a.Data))
	}
	return binary.BigEndian.Uint32(a.Data), nil
}

// Address decodes an IPv4 or IPv6 Address value.
func (a AVP) Address() (netip.Addr, error) {
	if len(a.Data) < 2 {
		return netip.Addr{}, fmt.Errorf("AVP %d: short Address", a.Code)
	}
	family, raw := binary.BigEndian.Uint16(a.Data), a.Data[2:]
	if (family == 1 && len(raw) == 4) || (family == 2 && len(raw) == 16) {
		addr, _ := netip.AddrFromSlice(raw)
		return addr, nil
	}
	return netip.Addr{}, fmt.Errorf("AVP %d: address family %d with %d octets", a.Code, family, len(raw))
}

// Grouped decodes the AVPs inside a Grouped AVP.
func (a AVP) Grouped() ([]AVP, error) {
	return ParseAVPs(a.Data)
}
