package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	d "github.com/alb777/diameter-ping/internal/diameter"
	"github.com/alb777/diameter-ping/internal/fakepeer"
	"github.com/alb777/diameter-ping/internal/peer"
)

func dping(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), append([]string{"-origin-host", "dping.test", "-i", "10ms"}, args...), &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestPingsAndDisconnects(t *testing.T) {
	addr := fakepeer.Listen(t, "tcp", &fakepeer.Peer{})
	code, out := dping(t, addr, "-c", "2")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"DPING " + addr + " over TCP as dping.test (realm test)",
		"CEA from peer.test: 2001 DIAMETER_SUCCESS",
		`realm test, product "fakepeer", vendor 0, host 127.0.0.1`,
		"DWA seq=1 from peer.test: 2001 DIAMETER_SUCCESS",
		"DWA seq=2 from peer.test: 2001 DIAMETER_SUCCESS",
		"DPA: 2001 DIAMETER_SUCCESS",
		"2 DWR sent, 2 DWA received, 0% loss, rtt min/avg/max",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}

func TestJSON(t *testing.T) {
	addr := fakepeer.Listen(t, "tcp", &fakepeer.Peer{DropDWR: 2})
	code, out := dping(t, "-c", "2", "-W", "200ms", "-json", addr)
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var rep report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep.CEA == nil || rep.CEA.ResultName != "DIAMETER_SUCCESS" || rep.DPA == nil || rep.DPA.ResultCode != 2001 {
		t.Errorf("CEA %+v, DPA %+v", rep.CEA, rep.DPA)
	}
	if rep.Sent != 2 || rep.Received != 1 || rep.LossPercent != 50 || rep.Watchdogs[0].Answer.ResultCode != 2001 ||
		!rep.Watchdogs[1].Timeout || rep.Watchdogs[1].Answer != nil {
		t.Errorf("sent %d, received %d, loss %v, watchdogs %+v", rep.Sent, rep.Received, rep.LossPercent, rep.Watchdogs)
	}
}

func TestExplainsFailures(t *testing.T) {
	refused := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return addr
	}
	cases := map[string]struct {
		addr string
		code int
		want []string
	}{
		"unknown peer": {fakepeer.Listen(t, "tcp", &fakepeer.Peer{CEAResult: 3010}), exitNoAnswer, []string{
			"CEA from peer.test: 3010 DIAMETER_UNKNOWN_PEER",
			"hint: the peer does not accept Origin-Host dping.test, realm test",
		}},
		"no common application": {fakepeer.Listen(t, "tcp", &fakepeer.Peer{CEAResult: 5010}), exitNoAnswer, []string{
			"5010 DIAMETER_NO_COMMON_APPLICATION", "hint: no application in common",
		}},
		"closed after CER": {fakepeer.Listen(t, "tcp", &fakepeer.Peer{CloseAfterCER: true}), exitNoAnswer, []string{
			"hint: the peer closed the connection without a CEA",
		}},
		"nothing listening": {refused(), exitError, []string{
			"connection refused", "hint: the host answered with a TCP reset",
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out := dping(t, "-c", "1", tc.addr)
			if code != tc.code {
				t.Errorf("exit %d, want %d", code, tc.code)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q\n%s", want, out)
				}
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	opts, err := parseArgs([]string{"hss.example.net", "-c", "3", "-sctp", "-app", "s6a,gy,16777238:10415",
		"-origin-host", "probe.epc.example"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	want := []peer.App{{ID: 16777251, VendorID: 10415}, {ID: 4}, {ID: 16777238, VendorID: 10415}}
	cfg := opts.cfg
	if cfg.Address != "hss.example.net:3868" || cfg.Network != "sctp" || opts.count != 3 ||
		cfg.OriginRealm != "epc.example" || len(cfg.Apps) != 3 || cfg.Apps[0] != want[0] || cfg.Apps[1] != want[1] ||
		cfg.Apps[2] != want[2] {
		t.Errorf("got %+v", opts)
	}
	for target, address := range map[string]string{
		"2001:db8::1":        "[2001:db8::1]:3868",
		"[2001:db8::1]:3869": "[2001:db8::1]:3869",
		"10.0.0.1:3869":      "10.0.0.1:3869",
	} {
		if got := withPort(target); got != address {
			t.Errorf("withPort(%q) = %q, want %q", target, got, address)
		}
	}
	for _, args := range [][]string{
		{},
		{"-c", "-1", "host"},
		{"-app", "s6x", "host"},
		{"-origin-host", "nodomain", "host"},
		{"host", "extra"},
	} {
		if _, err := parseArgs(args, &bytes.Buffer{}); err == nil {
			t.Errorf("parseArgs(%q) accepted bad input", args)
		}
	}
}

func TestWrap(t *testing.T) {
	long := strings.Fields(strings.Repeat("abcd ", 30))
	got := wrap("  hint: ", long)
	for _, line := range strings.Split(got, "\n") {
		if len(line) > width {
			t.Errorf("line of %d columns: %q", len(line), line)
		}
		if !strings.HasPrefix(line, "  hint: ") && !strings.HasPrefix(line, "        abcd") {
			t.Errorf("continuation not aligned: %q", line)
		}
	}
	if strings.Join(strings.Fields(strings.TrimPrefix(got, "  hint: ")), " ") != strings.Join(long, " ") {
		t.Error("wrap lost or split a token")
	}
}

func TestApplicationNamesAreInTheIANARegistry(t *testing.T) {
	for name, app := range appAliases {
		if d.ApplicationName(app.ID) == "" {
			t.Errorf("-app %s: application id %d is not in the IANA registry", name, app.ID)
		}
	}
}
