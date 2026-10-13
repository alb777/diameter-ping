package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/alb777/diameter-ping/internal/peer"
)

type stage int

const (
	stageConnect stage = iota
	stageCapabilities
	stageWatchdog
)

// errorHint says what usually causes a transport failure, or "" when there is nothing useful to add.
func errorHint(err error, cfg peer.Config, st stage) string {
	sctp := cfg.Network == "sctp"
	switch {
	case errors.Is(err, peer.ErrSCTPUnsupported):
		return "leave out -sctp, or run dping on Linux"
	case errors.Is(err, syscall.EPROTONOSUPPORT), errors.Is(err, syscall.ESOCKTNOSUPPORT):
		return "this kernel has no SCTP: load the sctp module (modprobe sctp) or leave out -sctp"
	case errors.Is(err, syscall.ECONNREFUSED) && sctp:
		return "the host answered with an SCTP ABORT: nothing listens on that port over SCTP. " +
			"Some peers only listen on TCP: leave out -sctp"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the host answered with a TCP reset: nothing listens on that port, or a firewall rejects " +
			"the connection. Some peers only listen on SCTP: try -sctp"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "no route to the peer: check the address and this host's routing"
	case errors.Is(err, peer.ErrPeerDisconnected):
		return "the peer asked to close the connection with a DPR, and dping answered it"
	case isTimeout(err) && st == stageConnect:
		return "no answer to the connection attempt: a firewall drops the packets, or the address or port is wrong"
	case isTimeout(err) && st == stageCapabilities:
		return "connected, but no CEA arrived: the port may not speak Diameter, or the peer ignores CERs " +
			"from this source address or Origin-Host"
	case isClosed(err) && st == stageCapabilities:
		return fmt.Sprintf("the peer closed the connection without a CEA. Many peers do that instead of "+
			"answering 3010 when the source address or Origin-Host (%s) is not in their peer list", cfg.OriginHost)
	case isClosed(err):
		return "the peer closed the connection"
	}
	return ""
}

// resultHint says what to check for the Result-Codes a capabilities exchange commonly fails with.
func resultHint(code uint32, cfg peer.Config) string {
	switch code {
	case 3004: // DIAMETER_TOO_BUSY
		return "the peer is too busy: retry later or on another peer"
	case 3010: // DIAMETER_UNKNOWN_PEER
		return fmt.Sprintf("the peer does not accept Origin-Host %s, realm %s or this source address: add them "+
			"to its peer list, or pass -origin-host and -origin-realm with an identity it knows",
			cfg.OriginHost, cfg.OriginRealm)
	case 4003: // ELECTION_LOST
		return "both sides opened a connection at the same time and the peer kept its own " +
			"(RFC 6733 section 5.6.4): retry"
	case 5010: // DIAMETER_NO_COMMON_APPLICATION
		return "no application in common: pass -app with one the peer serves, for example -app s6a " +
			"(its CEA lists them when it sends any)"
	case 5017: // DIAMETER_NO_COMMON_SECURITY
		return "the peer wants an inband security mechanism such as TLS, and dping speaks plain Diameter only"
	}
	return ""
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}

func isClosed(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
