package peer

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	d "github.com/alb777/diameter-ping/internal/diameter"
	"github.com/alb777/diameter-ping/internal/fakepeer"
)

var s6a = App{ID: 16777251, VendorID: 10415}

func connect(t *testing.T, network, addr string, timeout time.Duration) *Session {
	t.Helper()
	s, _, err := Connect(context.Background(), Config{
		Network: network, Address: addr, OriginHost: "dping.test", OriginRealm: "test",
		Apps: []App{s6a, {ID: 4}}, Timeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestFullExchange(t *testing.T) {
	for _, network := range []string{"tcp", "sctp"} {
		t.Run(network, func(t *testing.T) {
			p := &fakepeer.Peer{CEAExtra: []d.AVP{
				{Code: d.AuthApplicationID, Flags: d.FlagMandatory, Data: d.Uint32Data(0xffffffff)},
				{Code: d.VendorSpecificApplicationID, Flags: d.FlagMandatory, Data: d.GroupedData(
					d.AVP{Code: d.VendorID, Flags: d.FlagMandatory, Data: d.Uint32Data(10415)},
					d.AVP{Code: d.AuthApplicationID, Flags: d.FlagMandatory, Data: d.Uint32Data(16777251)},
				)},
			}}
			s := connect(t, network, fakepeer.Listen(t, network, p), time.Second)
			if s.Local.Addr() != netip.MustParseAddr("127.0.0.1") {
				t.Errorf("local address %v", s.Local)
			}

			caps, err := s.Capabilities()
			if err != nil {
				t.Fatal(err)
			}
			if caps.ResultCode != 2001 || caps.OriginHost != "peer.test" || caps.ProductName != "fakepeer" {
				t.Errorf("CEA: %+v", caps)
			}
			if want := []App{{ID: 0xffffffff}, s6a}; !slices.Equal(caps.Apps, want) {
				t.Errorf("CEA applications %v, want %v", caps.Apps, want)
			}
			if len(caps.HostIPs) != 1 || caps.HostIPs[0] != netip.MustParseAddr("127.0.0.1") {
				t.Errorf("CEA host IPs %v", caps.HostIPs)
			}
			if dwa, err := s.Watchdog(); err != nil || dwa.ResultCode != 2001 || dwa.RTT <= 0 {
				t.Fatalf("DWA %+v, %v", dwa, err)
			}
			if dpa, err := s.Disconnect(); err != nil || dpa.ResultCode != 2001 {
				t.Fatalf("DPA %+v, %v", dpa, err)
			}

			got := p.Received()
			if codes := []uint32{got[0].Code, got[1].Code, got[2].Code}; !slices.Equal(codes,
				[]uint32{d.CapabilitiesExchange, d.DeviceWatchdog, d.DisconnectPeer}) {
				t.Fatalf("peer received commands %v", codes)
			}
			checkCER(t, got[0])
			cause, _ := d.Find(got[2].AVPs, d.DisconnectCause)
			if v, _ := cause.Uint32(); v != doNotWantToTalkToYou {
				t.Errorf("Disconnect-Cause %d", v)
			}
		})
	}
}

// checkCER holds the CER to RFC 6733 section 5.3.1 and to how 3GPP applications are advertised.
func checkCER(t *testing.T, cer *d.Message) {
	t.Helper()
	if !cer.IsRequest() || cer.AppID != 0 {
		t.Errorf("CER flags %x, application %d", cer.Flags, cer.AppID)
	}
	for _, code := range []uint32{d.OriginHost, d.OriginRealm, d.HostIPAddress, d.VendorID, d.ProductName} {
		if _, ok := d.Find(cer.AVPs, code); !ok {
			t.Errorf("CER without %s", d.AVPName(code))
		}
	}
	if host, _ := d.Find(cer.AVPs, d.HostIPAddress); !slices.Equal(host.Data, d.AddressData(netip.MustParseAddr("127.0.0.1"))) {
		t.Errorf("Host-IP-Address %x", host.Data)
	}
	if product, _ := d.Find(cer.AVPs, d.ProductName); product.Flags&d.FlagMandatory != 0 {
		t.Error("Product-Name has the M bit set")
	}
	if v, _ := d.Find(cer.AVPs, d.SupportedVendorID); string(v.Data) != string(d.Uint32Data(10415)) {
		t.Error("CER without Supported-Vendor-Id 10415")
	}
	if a, _ := d.Find(cer.AVPs, d.AuthApplicationID); string(a.Data) != string(d.Uint32Data(4)) {
		t.Error("IETF application 4 not sent as a plain Auth-Application-Id")
	}
	vsai, ok := d.Find(cer.AVPs, d.VendorSpecificApplicationID)
	inner, err := vsai.Grouped()
	if !ok || err != nil {
		t.Fatalf("CER without a Vendor-Specific-Application-Id: %v", err)
	}
	vendor, _ := d.Find(inner, d.VendorID)
	app, _ := d.Find(inner, d.AuthApplicationID)
	if v, _ := vendor.Uint32(); v != 10415 {
		t.Errorf("Vendor-Specific-Application-Id vendor %d", v)
	}
	if id, _ := app.Uint32(); id != 16777251 {
		t.Errorf("Vendor-Specific-Application-Id application %d", id)
	}
}

func TestRejectedCapabilitiesAreAnAnswerNotAnError(t *testing.T) {
	s := connect(t, "tcp", fakepeer.Listen(t, "tcp", &fakepeer.Peer{CEAResult: 3010}), time.Second)
	caps, err := s.Capabilities()
	if err != nil || caps.ResultCode != 3010 {
		t.Fatalf("got %+v, %v", caps, err)
	}
}

func TestPeerClosingAfterCER(t *testing.T) {
	s := connect(t, "tcp", fakepeer.Listen(t, "tcp", &fakepeer.Peer{CloseAfterCER: true}), time.Second)
	if _, err := s.Capabilities(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
}

func TestWatchdogTimeoutThenNextAnswer(t *testing.T) {
	s := connect(t, "tcp", fakepeer.Listen(t, "tcp", &fakepeer.Peer{DropDWR: 1}), 200*time.Millisecond)
	if _, err := s.Capabilities(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Watchdog(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("first DWR: got %v, want a timeout", err)
	}
	if dwa, err := s.Watchdog(); err != nil || dwa.ResultCode != 2001 {
		t.Fatalf("second DWR: %+v, %v", dwa, err)
	}
}

func TestAnswersTheWatchdogOfThePeer(t *testing.T) {
	p := &fakepeer.Peer{AskDWR: true}
	s := connect(t, "tcp", fakepeer.Listen(t, "tcp", p), time.Second)
	if _, err := s.Capabilities(); err != nil {
		t.Fatal(err)
	}
	if dwa, err := s.Watchdog(); err != nil || dwa.ResultCode != 2001 {
		t.Fatalf("DWA %+v, %v", dwa, err)
	}
	if _, err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for _, m := range p.Received() {
		if !m.IsRequest() && m.Code == d.DeviceWatchdog && m.HopByHop == 7 {
			if rc, _ := d.Find(m.AVPs, d.ResultCode); string(rc.Data) == string(d.Uint32Data(2001)) {
				return
			}
		}
	}
	t.Fatal("dping did not answer the peer's DWR with a 2001 DWA")
}
