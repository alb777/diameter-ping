// Package interop runs dping against go-diameter, a Diameter implementation that shares no code with dping,
// so dping's messages are checked by someone else's parser. It is a module of its own to keep dping free of
// dependencies.
package interop

import (
	"net"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"github.com/fiorix/go-diameter/v4/diam/sm"
)

// Start serves a go-diameter peer on address ("127.0.0.1:0" for any port) over "tcp" or "sctp". It answers
// CER, DWR and DPR with the base dictionary's applications. Close the listener to stop it.
func Start(network, address, originHost, originRealm string) (net.Listener, <-chan *diam.ErrorReport, error) {
	mux := sm.New(&sm.Settings{
		OriginHost:  datatype.DiameterIdentity(originHost),
		OriginRealm: datatype.DiameterIdentity(originRealm),
		ProductName: "go-diameter",
	})
	// go-diameter's state machine handles CER and DWR but not DPR, so answer it here (RFC 6733 section 5.4.2).
	mux.HandleFunc("DPR", func(c diam.Conn, m *diam.Message) {
		a := m.Answer(diam.Success)
		a.NewAVP(avp.OriginHost, avp.Mbit, 0, datatype.DiameterIdentity(originHost))
		a.NewAVP(avp.OriginRealm, avp.Mbit, 0, datatype.DiameterIdentity(originRealm))
		a.WriteTo(c)
	})
	ln, err := diam.Listen(network, address)
	if err != nil {
		return nil, nil, err
	}
	srv := &diam.Server{Network: network, Handler: mux, Dict: dict.Default}
	go srv.Serve(ln)
	return ln, mux.ErrorReports(), nil
}
