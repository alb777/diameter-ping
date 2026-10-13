package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	d "github.com/alb777/diameter-ping/internal/diameter"
	"github.com/alb777/diameter-ping/internal/peer"
)

// width is where text output wraps long hints and lists.
const width = 100

// reporter prints a run as ping-like text while it happens, or as one JSON object at the end.
type reporter struct {
	w    io.Writer
	opts *options
	sent int
	rtts []time.Duration
	rep  report
}

type report struct {
	Target      string         `json:"target"`
	Transport   string         `json:"transport"`
	OriginHost  string         `json:"origin_host"`
	OriginRealm string         `json:"origin_realm"`
	Local       string         `json:"local,omitempty"`
	Remote      string         `json:"remote,omitempty"`
	ConnectMS   float64        `json:"connect_ms,omitempty"`
	CEA         *ceaJSON       `json:"cea,omitempty"`
	Watchdogs   []watchdogJSON `json:"watchdogs"`
	DPA         *answerJSON    `json:"dpa,omitempty"`
	Sent        int            `json:"sent"`
	Received    int            `json:"received"`
	LossPercent float64        `json:"loss_percent"`
	RTTMS       *rttJSON       `json:"rtt_ms,omitempty"`
	Error       string         `json:"error,omitempty"`
	Hint        string         `json:"hint,omitempty"`
}

type answerJSON struct {
	ResultCode   uint32   `json:"result_code"`
	ResultName   string   `json:"result_name,omitempty"`
	OriginHost   string   `json:"origin_host,omitempty"`
	OriginRealm  string   `json:"origin_realm,omitempty"`
	ErrorMessage string   `json:"error_message,omitempty"`
	FailedAVPs   []uint32 `json:"failed_avps,omitempty"`
	RTTMS        float64  `json:"rtt_ms"`
}

type ceaJSON struct {
	answerJSON
	ProductName  string    `json:"product_name,omitempty"`
	VendorID     uint32    `json:"vendor_id"`
	HostIPs      []string  `json:"host_ips,omitempty"`
	Applications []appJSON `json:"applications,omitempty"`
	Hint         string    `json:"hint,omitempty"`
}

type appJSON struct {
	ID       uint32 `json:"id"`
	VendorID uint32 `json:"vendor_id,omitempty"`
	Name     string `json:"name,omitempty"`
}

type watchdogJSON struct {
	Seq     int         `json:"seq"`
	Answer  *answerJSON `json:"answer,omitempty"`
	Timeout bool        `json:"timeout,omitempty"`
}

type rttJSON struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
}

func newReporter(w io.Writer, opts *options) *reporter {
	return &reporter{w: w, opts: opts, rep: report{
		Target:      opts.target,
		Transport:   opts.cfg.Network,
		OriginHost:  opts.cfg.OriginHost,
		OriginRealm: opts.cfg.OriginRealm,
		Watchdogs:   []watchdogJSON{},
	}}
}

func (r *reporter) linef(format string, args ...any) {
	if !r.opts.json {
		fmt.Fprintf(r.w, format+"\n", args...)
	}
}

func (r *reporter) start() {
	r.linef("DPING %s over %s as %s (realm %s)", r.opts.cfg.Address, strings.ToUpper(r.opts.cfg.Network),
		r.opts.cfg.OriginHost, r.opts.cfg.OriginRealm)
}

func (r *reporter) connected(local, remote netip.AddrPort, took time.Duration) {
	r.rep.Local, r.rep.Remote, r.rep.ConnectMS = local.String(), remote.String(), ms(took)
	r.linef("connected to %s from %s in %.3f ms", remote, local, ms(took))
}

func (r *reporter) capabilities(c *peer.Capabilities, hint string) {
	cea := &ceaJSON{answerJSON: answerOf(&c.Answer), ProductName: c.ProductName, VendorID: c.VendorID, Hint: hint}
	for _, ip := range c.HostIPs {
		cea.HostIPs = append(cea.HostIPs, ip.String())
	}
	apps := make([]string, 0, len(c.Apps))
	for _, app := range c.Apps {
		cea.Applications = append(cea.Applications, appJSON{ID: app.ID, VendorID: app.VendorID, Name: d.ApplicationName(app.ID)})
		apps = append(apps, app.String())
	}
	r.rep.CEA = cea
	r.linef("CEA from %s: %s, %.3f ms", c.OriginHost, resultText(c.ResultCode), ms(c.RTT))
	r.linef("  realm %s, product %q, vendor %d, host %s", c.OriginRealm, c.ProductName, c.VendorID,
		strings.Join(cea.HostIPs, " "))
	if len(apps) > 0 {
		for i := 0; i < len(apps)-1; i++ {
			apps[i] += "," // wrap breaks between applications, never inside one
		}
		r.linef("%s", wrap("  applications: ", apps))
	}
	r.details(&c.Answer, hint)
}

func (r *reporter) watchdog(seq int, a *peer.Answer) {
	r.sent++
	r.rtts = append(r.rtts, a.RTT)
	aj := answerOf(a)
	r.rep.Watchdogs = append(r.rep.Watchdogs, watchdogJSON{Seq: seq, Answer: &aj})
	r.linef("DWA seq=%d from %s: %s, %.3f ms", seq, a.OriginHost, resultText(a.ResultCode), ms(a.RTT))
	r.details(a, "")
}

func (r *reporter) watchdogTimeout(seq int, timeout time.Duration) {
	r.sent++
	r.rep.Watchdogs = append(r.rep.Watchdogs, watchdogJSON{Seq: seq, Timeout: true})
	r.linef("DWA seq=%d: no answer in %s", seq, timeout)
}

func (r *reporter) details(a *peer.Answer, hint string) {
	if a.ErrorMessage != "" {
		r.linef("%s", wrap("  error message: ", strings.Fields(a.ErrorMessage)))
	}
	for _, code := range a.FailedAVPs {
		r.linef("  failed AVP: %d %s", code, d.AVPName(code))
	}
	if hint != "" {
		r.linef("%s", wrap("  hint: ", strings.Fields(hint)))
	}
}

func (r *reporter) failure(err error, hint string) {
	r.rep.Error, r.rep.Hint = err.Error(), hint
	r.linef("error: %v", err)
	if hint != "" {
		r.linef("%s", wrap("hint: ", strings.Fields(hint)))
	}
}

func (r *reporter) disconnect(a *peer.Answer, err error) {
	switch {
	case isTimeout(err):
		r.linef("DPA: no answer in %s", r.opts.cfg.Timeout)
		return
	case err != nil:
		r.linef("DPA: no answer (%v)", err)
		return
	}
	aj := answerOf(a)
	r.rep.DPA = &aj
	r.linef("DPA: %s", resultText(a.ResultCode))
}

func (r *reporter) finish() {
	r.rep.Sent, r.rep.Received = r.sent, len(r.rtts)
	if r.sent > 0 {
		r.rep.LossPercent = float64(r.sent-len(r.rtts)) * 100 / float64(r.sent)
	}
	if len(r.rtts) > 0 {
		lo, hi, sum := r.rtts[0], r.rtts[0], time.Duration(0)
		for _, rtt := range r.rtts {
			lo, hi, sum = min(lo, rtt), max(hi, rtt), sum+rtt
		}
		r.rep.RTTMS = &rttJSON{Min: ms(lo), Avg: ms(sum / time.Duration(len(r.rtts))), Max: ms(hi)}
	}
	if r.opts.json {
		b, _ := json.MarshalIndent(r.rep, "", "  ")
		fmt.Fprintln(r.w, string(b))
		return
	}
	if r.sent == 0 {
		return
	}
	stats := fmt.Sprintf("%d DWR sent, %d DWA received, %.0f%% loss", r.sent, len(r.rtts), r.rep.LossPercent)
	if rtt := r.rep.RTTMS; rtt != nil {
		stats += fmt.Sprintf(", rtt min/avg/max = %.3f/%.3f/%.3f ms", rtt.Min, rtt.Avg, rtt.Max)
	}
	r.linef("\n--- %s dping statistics ---\n%s", r.opts.cfg.Address, stats)
}

func answerOf(a *peer.Answer) answerJSON {
	return answerJSON{
		ResultCode:   a.ResultCode,
		ResultName:   d.ResultName(a.ResultCode),
		OriginHost:   a.OriginHost,
		OriginRealm:  a.OriginRealm,
		ErrorMessage: a.ErrorMessage,
		FailedAVPs:   a.FailedAVPs,
		RTTMS:        ms(a.RTT),
	}
}

// wrap joins tokens with spaces into lines of at most width columns, never splitting a token, so a long
// hint or application list stays readable in a terminal. Continuation lines start under the first token.
func wrap(prefix string, tokens []string) string {
	var b strings.Builder
	b.WriteString(prefix)
	col := len(prefix)
	for i, tok := range tokens {
		if i > 0 {
			if col+1+len(tok) > width {
				b.WriteString("\n" + strings.Repeat(" ", len(prefix)))
				col = len(prefix)
			} else {
				b.WriteString(" ")
				col++
			}
		}
		b.WriteString(tok)
		col += len(tok)
	}
	return b.String()
}

func resultText(code uint32) string {
	if name := d.ResultName(code); name != "" {
		return fmt.Sprintf("%d %s", code, name)
	}
	return fmt.Sprintf("%d (not assigned)", code)
}

// ms is a duration in milliseconds, rounded to the microsecond.
func ms(t time.Duration) float64 {
	return float64(t.Microseconds()) / 1000
}
