package peer

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"time"

	d "github.com/alb777/diameter-ping/internal/diameter"
)

// ProductName goes into the CER (RFC 6733 section 5.3.7).
const ProductName = "dping"

// Disconnect-Cause DO_NOT_WANT_TO_TALK_TO_YOU (RFC 6733 section 5.4.3): no further messages are expected,
// which is exactly a ping that is done.
const doNotWantToTalkToYou = 2

// Codes dping answers with when the peer sends it a request.
const (
	resultSuccess            = 2001
	resultCommandUnsupported = 3001
)

// ErrPeerDisconnected means the peer sent a DPR, which dping answered before stopping.
var ErrPeerDisconnected = errors.New("the peer sent a Disconnect-Peer-Request")

// Config is where dping connects and how it introduces itself.
type Config struct {
	Network     string // "tcp" or "sctp"
	Address     string // host:port
	OriginHost  string
	OriginRealm string
	Apps        []App
	Timeout     time.Duration // for the connection and for each answer
}

// App is a Diameter application to advertise. VendorID 0 is an IETF application; others are sent as a
// Vendor-Specific-Application-Id, the way 3GPP applications are.
type App struct {
	ID       uint32
	VendorID uint32
}

// Answer is what dping reads from any answer.
type Answer struct {
	ResultCode   uint32
	OriginHost   string
	OriginRealm  string
	ErrorMessage string
	FailedAVPs   []uint32 // codes of the AVPs inside Failed-AVP
	RTT          time.Duration
}

// Capabilities is a Capabilities-Exchange-Answer.
type Capabilities struct {
	Answer
	ProductName string
	VendorID    uint32
	HostIPs     []netip.Addr
	Apps        []App
}

// Session is one transport connection to a peer.
type Session struct {
	c        conn
	cfg      Config
	Local    netip.AddrPort
	Remote   netip.AddrPort
	hopByHop uint32
	endToEnd uint32
}

// Connect opens the transport connection. It returns how long the connection took.
func Connect(ctx context.Context, cfg Config) (*Session, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	start := time.Now()
	c, local, remote, err := dial(ctx, cfg.Network, cfg.Address)
	if err != nil {
		return nil, 0, err
	}
	s := &Session{
		c:        c,
		cfg:      cfg,
		Local:    local,
		Remote:   remote,
		hopByHop: rand.Uint32(),
		// RFC 6733 section 3: the high 12 bits come from the clock, the low 20 bits are random.
		endToEnd: uint32(time.Now().Unix())<<20 | rand.Uint32()&0xfffff,
	}
	return s, time.Since(start), nil
}

// Close closes the transport connection without a DPR.
func (s *Session) Close() error {
	return s.c.Close()
}

// Capabilities sends the CER and reads the CEA.
func (s *Session) Capabilities() (*Capabilities, error) {
	avps := []d.AVP{
		{Code: d.OriginHost, Flags: d.FlagMandatory, Data: []byte(s.cfg.OriginHost)},
		{Code: d.OriginRealm, Flags: d.FlagMandatory, Data: []byte(s.cfg.OriginRealm)},
		{Code: d.HostIPAddress, Flags: d.FlagMandatory, Data: d.AddressData(s.Local.Addr())},
		{Code: d.VendorID, Flags: d.FlagMandatory, Data: d.Uint32Data(0)},
		{Code: d.ProductName, Data: []byte(ProductName)}, // the M bit must not be set
	}
	vendors := map[uint32]bool{}
	for _, app := range s.cfg.Apps {
		if app.VendorID == 0 {
			avps = append(avps, d.AVP{Code: d.AuthApplicationID, Flags: d.FlagMandatory, Data: d.Uint32Data(app.ID)})
			continue
		}
		if !vendors[app.VendorID] {
			vendors[app.VendorID] = true
			avps = append(avps, d.AVP{Code: d.SupportedVendorID, Flags: d.FlagMandatory, Data: d.Uint32Data(app.VendorID)})
		}
		avps = append(avps, d.AVP{Code: d.VendorSpecificApplicationID, Flags: d.FlagMandatory, Data: d.GroupedData(
			d.AVP{Code: d.VendorID, Flags: d.FlagMandatory, Data: d.Uint32Data(app.VendorID)},
			d.AVP{Code: d.AuthApplicationID, Flags: d.FlagMandatory, Data: d.Uint32Data(app.ID)},
		)})
	}
	m, rtt, err := s.exchange(d.CapabilitiesExchange, avps)
	if err != nil {
		return nil, err
	}
	caps := &Capabilities{Answer: answer(m, rtt)}
	if a, ok := d.Find(m.AVPs, d.ProductName); ok {
		caps.ProductName = string(a.Data)
	}
	if a, ok := d.Find(m.AVPs, d.VendorID); ok {
		caps.VendorID, _ = a.Uint32()
	}
	for _, a := range m.AVPs {
		if a.Flags&d.FlagVendor != 0 {
			continue
		}
		switch a.Code {
		case d.HostIPAddress:
			if ip, err := a.Address(); err == nil {
				caps.HostIPs = append(caps.HostIPs, ip)
			}
		case d.AuthApplicationID, d.AcctApplicationID:
			if id, err := a.Uint32(); err == nil {
				caps.Apps = addApp(caps.Apps, App{ID: id})
			}
		case d.VendorSpecificApplicationID:
			caps.Apps = addVendorApp(caps.Apps, a)
		}
	}
	return caps, nil
}

// Watchdog sends a DWR and reads the DWA.
func (s *Session) Watchdog() (*Answer, error) {
	m, rtt, err := s.exchange(d.DeviceWatchdog, s.origin())
	if err != nil {
		return nil, err
	}
	a := answer(m, rtt)
	return &a, nil
}

// Disconnect sends a DPR, reads the DPA and closes the connection.
func (s *Session) Disconnect() (*Answer, error) {
	defer s.c.Close()
	avps := append(s.origin(), d.AVP{Code: d.DisconnectCause, Flags: d.FlagMandatory, Data: d.Uint32Data(doNotWantToTalkToYou)})
	m, rtt, err := s.exchange(d.DisconnectPeer, avps)
	if err != nil {
		return nil, err
	}
	a := answer(m, rtt)
	return &a, nil
}

func (s *Session) origin() []d.AVP {
	return []d.AVP{
		{Code: d.OriginHost, Flags: d.FlagMandatory, Data: []byte(s.cfg.OriginHost)},
		{Code: d.OriginRealm, Flags: d.FlagMandatory, Data: []byte(s.cfg.OriginRealm)},
	}
}

// exchange sends a base protocol request and waits for its answer, answering the peer's own requests meanwhile.
func (s *Session) exchange(code uint32, avps []d.AVP) (*d.Message, time.Duration, error) {
	s.hopByHop++
	s.endToEnd++
	req := &d.Message{Flags: d.FlagRequest, Code: code, HopByHop: s.hopByHop, EndToEnd: s.endToEnd, AVPs: avps}
	start := time.Now()
	if err := s.c.SetDeadline(start.Add(s.cfg.Timeout)); err != nil {
		return nil, 0, err
	}
	if _, err := s.c.Write(req.Marshal()); err != nil {
		return nil, 0, err
	}
	for {
		m, err := d.ReadMessage(s.c)
		if err != nil {
			return nil, 0, err
		}
		if m.IsRequest() {
			if err := s.answerPeer(m); err != nil {
				return nil, 0, err
			}
			continue
		}
		if m.Code == code && m.HopByHop == req.HopByHop {
			return m, time.Since(start), nil
		}
		// An answer to an earlier request that timed out: drop it.
	}
}

// answerPeer answers a request the peer sent: DWR and DPR as RFC 6733 says, anything else as unsupported.
func (s *Session) answerPeer(req *d.Message) error {
	result := uint32(resultSuccess)
	flags := req.Flags & d.FlagProxiable
	if req.Code != d.DeviceWatchdog && req.Code != d.DisconnectPeer {
		result = resultCommandUnsupported
		flags |= d.FlagError
	}
	ans := &d.Message{
		Flags:    flags,
		Code:     req.Code,
		AppID:    req.AppID,
		HopByHop: req.HopByHop,
		EndToEnd: req.EndToEnd,
		AVPs: append([]d.AVP{{Code: d.ResultCode, Flags: d.FlagMandatory, Data: d.Uint32Data(result)}},
			s.origin()...),
	}
	if _, err := s.c.Write(ans.Marshal()); err != nil {
		return err
	}
	if req.Code == d.DisconnectPeer {
		return ErrPeerDisconnected
	}
	return nil
}

func answer(m *d.Message, rtt time.Duration) Answer {
	a := Answer{RTT: rtt}
	if avp, ok := d.Find(m.AVPs, d.ResultCode); ok {
		a.ResultCode, _ = avp.Uint32()
	}
	if avp, ok := d.Find(m.AVPs, d.OriginHost); ok {
		a.OriginHost = string(avp.Data)
	}
	if avp, ok := d.Find(m.AVPs, d.OriginRealm); ok {
		a.OriginRealm = string(avp.Data)
	}
	if avp, ok := d.Find(m.AVPs, d.ErrorMessage); ok {
		a.ErrorMessage = string(avp.Data)
	}
	if avp, ok := d.Find(m.AVPs, d.FailedAVP); ok {
		if inner, err := avp.Grouped(); err == nil {
			for _, i := range inner {
				a.FailedAVPs = append(a.FailedAVPs, i.Code)
			}
		}
	}
	return a
}

func addVendorApp(apps []App, vsai d.AVP) []App {
	inner, err := vsai.Grouped()
	if err != nil {
		return apps
	}
	var vendor uint32
	if a, ok := d.Find(inner, d.VendorID); ok {
		vendor, _ = a.Uint32()
	}
	for _, a := range inner {
		if a.Code == d.AuthApplicationID || a.Code == d.AcctApplicationID {
			if id, err := a.Uint32(); err == nil {
				apps = addApp(apps, App{ID: id, VendorID: vendor})
			}
		}
	}
	return apps
}

func addApp(apps []App, app App) []App {
	for _, a := range apps {
		if a == app {
			return apps
		}
	}
	return append(apps, app)
}

// String names an application the way dping prints it: "3GPP S6a (16777251, vendor 10415)".
func (a App) String() string {
	name := d.ApplicationName(a.ID)
	if name == "" {
		name = "application"
	}
	if a.VendorID != 0 {
		return fmt.Sprintf("%s (%d, vendor %d)", name, a.ID, a.VendorID)
	}
	return fmt.Sprintf("%s (%d)", name, a.ID)
}
