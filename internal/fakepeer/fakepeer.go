// Package fakepeer is a scripted Diameter peer for dping's tests. It serves one connection.
package fakepeer

import (
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"

	d "github.com/alb777/diameter-ping/internal/diameter"
)

// Peer says how to answer. The zero value answers everything with DIAMETER_SUCCESS.
type Peer struct {
	CEAResult     uint32  // Result-Code of the CEA; 0 means 2001
	CEAExtra      []d.AVP // appended to the CEA, for example the peer's applications
	CloseAfterCER bool    // close the connection instead of answering the CER
	DropDWR       int     // never answer this DWR (1 is the first)
	AskDWR        bool    // send a DWR of its own before answering the first DWR

	mu       sync.Mutex
	received []*d.Message
	done     chan struct{}
}

// Received returns the messages the peer read, once the connection is over.
func (p *Peer) Received() []*d.Message {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.received
}

// Listen starts the peer on 127.0.0.1 and returns the address to dial. network is "tcp" or "sctp".
func Listen(t testing.TB, network string, p *Peer) string {
	t.Helper()
	p.done = make(chan struct{})
	var accept func() (io.ReadWriteCloser, error)
	var addr string
	switch network {
	case "tcp":
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		accept = func() (io.ReadWriteCloser, error) { return ln.Accept() }
		addr = ln.Addr().String()
	case "sctp":
		accept, addr = listenSCTP(t)
	default:
		t.Fatalf("unknown network %q", network)
	}
	go func() {
		defer close(p.done)
		c, err := accept()
		if err != nil {
			return
		}
		defer c.Close()
		p.serve(c)
	}()
	return addr
}

func (p *Peer) serve(c io.ReadWriteCloser) {
	dwr := 0
	for {
		m, err := d.ReadMessage(c)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.received = append(p.received, m)
		p.mu.Unlock()
		if !m.IsRequest() {
			continue // dping's answer to our own DWR
		}
		switch m.Code {
		case d.CapabilitiesExchange:
			if p.CloseAfterCER {
				return
			}
			result := p.CEAResult
			if result == 0 {
				result = 2001
			}
			avps := append([]d.AVP{
				{Code: d.HostIPAddress, Flags: d.FlagMandatory, Data: d.AddressData(netip.MustParseAddr("127.0.0.1"))},
				{Code: d.VendorID, Flags: d.FlagMandatory, Data: d.Uint32Data(0)},
				{Code: d.ProductName, Data: []byte("fakepeer")},
			}, p.CEAExtra...)
			write(c, answerTo(m, result, avps...))
		case d.DeviceWatchdog:
			dwr++
			if dwr == p.DropDWR {
				continue
			}
			if p.AskDWR && dwr == 1 {
				write(c, &d.Message{Flags: d.FlagRequest, Code: d.DeviceWatchdog, HopByHop: 7, EndToEnd: 7, AVPs: origin()})
			}
			write(c, answerTo(m, 2001))
		case d.DisconnectPeer:
			write(c, answerTo(m, 2001))
			return
		}
	}
}

func answerTo(req *d.Message, result uint32, extra ...d.AVP) *d.Message {
	avps := append([]d.AVP{{Code: d.ResultCode, Flags: d.FlagMandatory, Data: d.Uint32Data(result)}}, origin()...)
	return &d.Message{
		Code:     req.Code,
		AppID:    req.AppID,
		HopByHop: req.HopByHop,
		EndToEnd: req.EndToEnd,
		AVPs:     append(avps, extra...),
	}
}

func origin() []d.AVP {
	return []d.AVP{
		{Code: d.OriginHost, Flags: d.FlagMandatory, Data: []byte("peer.test")},
		{Code: d.OriginRealm, Flags: d.FlagMandatory, Data: []byte("test")},
	}
}

// write ignores errors: a broken connection shows up as a failed read in serve's next turn.
func write(c io.Writer, m *d.Message) {
	_, _ = c.Write(m.Marshal())
}
