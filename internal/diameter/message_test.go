package diameter

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
)

func TestMarshalMatchesRFC6733Layout(t *testing.T) {
	// A DWR worked out by hand from RFC 6733 sections 3 and 4: 20 octet header, then two AVPs whose values
	// are padded to 32 bits while the AVP Length leaves the padding out.
	m := &Message{Flags: FlagRequest, Code: DeviceWatchdog, HopByHop: 1, EndToEnd: 2, AVPs: []AVP{
		{Code: OriginHost, Flags: FlagMandatory, Data: []byte("a.b")},
		{Code: OriginRealm, Flags: FlagMandatory, Data: []byte("b")},
	}}
	want := "0100002c" + "80000118" + "00000000" + "00000001" + "00000002" +
		"00000108" + "4000000b" + "612e6200" +
		"00000128" + "40000009" + "62000000"
	if got := hex.EncodeToString(m.Marshal()); got != want {
		t.Fatalf("Marshal\n got %s\nwant %s", got, want)
	}
}

func TestRoundTripWithVendorAndGroupedAVPs(t *testing.T) {
	in := &Message{Flags: FlagProxiable, Code: CapabilitiesExchange, AppID: 0, HopByHop: 0xdeadbeef, EndToEnd: 42,
		AVPs: []AVP{
			{Code: ResultCode, Flags: FlagMandatory, Data: Uint32Data(2001)},
			{Code: 1407, Flags: FlagVendor | FlagMandatory, VendorID: 10415, Data: []byte{0x62, 0xf2, 0x10}},
			{Code: VendorSpecificApplicationID, Flags: FlagMandatory, Data: GroupedData(
				AVP{Code: VendorID, Flags: FlagMandatory, Data: Uint32Data(10415)},
				AVP{Code: AuthApplicationID, Flags: FlagMandatory, Data: Uint32Data(16777251)},
			)},
		}}
	out, err := ReadMessage(bytes.NewReader(in.Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	if out.Flags != in.Flags || out.Code != in.Code || out.HopByHop != in.HopByHop || out.EndToEnd != in.EndToEnd {
		t.Fatalf("header: got %+v", out)
	}
	if len(out.AVPs) != 3 {
		t.Fatalf("got %d AVPs", len(out.AVPs))
	}
	if v := out.AVPs[1]; v.VendorID != 10415 || !bytes.Equal(v.Data, []byte{0x62, 0xf2, 0x10}) {
		t.Fatalf("vendor AVP: got %+v", v)
	}
	if _, ok := Find(out.AVPs, 1407); ok {
		t.Fatal("Find returned a vendor-specific AVP")
	}
	inner, err := out.AVPs[2].Grouped()
	if err != nil {
		t.Fatal(err)
	}
	app, ok := Find(inner, AuthApplicationID)
	if id, _ := app.Uint32(); !ok || id != 16777251 {
		t.Fatalf("grouped Auth-Application-Id: got %d", id)
	}
}

func TestReadMessageRejectsMalformedInput(t *testing.T) {
	valid := (&Message{Code: DeviceWatchdog, AVPs: []AVP{{Code: OriginHost, Data: []byte("x")}}}).Marshal()
	withLength := func(n int) []byte {
		b := bytes.Clone(valid)
		b[1], b[2], b[3] = byte(n>>16), byte(n>>8), byte(n)
		return b
	}
	cases := map[string]struct {
		input []byte
		want  string
	}{
		"version 2":          {append([]byte{2}, valid[1:]...), "version 2"},
		"length below 20":    {withLength(16), "length 16"},
		"length not aligned": {withLength(30), "length 30"},
		"bad AVP length":     {append(bytes.Clone(valid[:20+4]), 0, 0, 0, 0x40, 0, 0, 0, 0), "bad length"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ReadMessage(bytes.NewReader(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, err := ReadMessage(bytes.NewReader(valid[:len(valid)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated body: got %v", err)
	}
	if _, err := ReadMessage(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream: got %v", err)
	}
}

func TestAddress(t *testing.T) {
	for _, tc := range []struct{ addr, hex string }{
		{"192.0.2.1", "0001c0000201"},
		{"::ffff:192.0.2.1", "0001c0000201"}, // a mapped address goes on the wire as IPv4
		{"2001:db8::1", "000220010db8000000000000000000000001"},
	} {
		data := AddressData(netip.MustParseAddr(tc.addr))
		if got := hex.EncodeToString(data); got != tc.hex {
			t.Errorf("AddressData(%s) = %s, want %s", tc.addr, got, tc.hex)
		}
		got, err := AVP{Data: data}.Address()
		if err != nil || got != netip.MustParseAddr(tc.addr).Unmap() {
			t.Errorf("Address() = %v, %v", got, err)
		}
	}
	if _, err := (AVP{Data: []byte{0, 1, 1, 2, 3}}).Address(); err == nil {
		t.Error("an IPv4 family with 3 octets was accepted")
	}
}

func TestIANANames(t *testing.T) {
	for code, want := range map[uint32]string{
		2001: "DIAMETER_SUCCESS",
		3010: "DIAMETER_UNKNOWN_PEER",
		5010: "DIAMETER_NO_COMMON_APPLICATION",
		9999: "",
	} {
		if got := ResultName(code); got != want {
			t.Errorf("ResultName(%d) = %q, want %q", code, got, want)
		}
	}
	for id, want := range map[uint32]string{16777251: "3GPP S6a", 4: "Diameter Credit Control", 0xffffffff: "Relay"} {
		if got := ApplicationName(id); got != want {
			t.Errorf("ApplicationName(%d) = %q, want %q", id, got, want)
		}
	}
	if !strings.HasPrefix(IANASource(), "IANA AAA Parameters") {
		t.Errorf("IANASource() = %q", IANASource())
	}
}
