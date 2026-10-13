// Command dping checks a Diameter peer the way ping checks a host: it connects over TCP or SCTP, exchanges
// capabilities (CER/CEA), sends Device-Watchdog-Requests, prints each answer and its round-trip time, and
// disconnects cleanly (DPR/DPA).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	d "github.com/alb777/diameter-ping/internal/diameter"
	"github.com/alb777/diameter-ping/internal/peer"
)

const (
	version     = "0.1.0"
	defaultPort = "3868"
	vendor3GPP  = 10415
)

// Exit statuses, as in ping: 0 the peer answered, 1 it did not accept or answer, 2 anything else.
const (
	exitOK       = 0
	exitNoAnswer = 1
	exitError    = 2
)

// appAliases are interface names for the application ids in the IANA registry.
var appAliases = map[string]peer.App{
	"cx":    {ID: 16777216, VendorID: vendor3GPP},
	"sh":    {ID: 16777217, VendorID: vendor3GPP},
	"rx":    {ID: 16777236, VendorID: vendor3GPP},
	"gx":    {ID: 16777238, VendorID: vendor3GPP},
	"s6a":   {ID: 16777251, VendorID: vendor3GPP},
	"s6d":   {ID: 16777251, VendorID: vendor3GPP},
	"s13":   {ID: 16777252, VendorID: vendor3GPP},
	"slg":   {ID: 16777255, VendorID: vendor3GPP},
	"swm":   {ID: 16777264, VendorID: vendor3GPP},
	"swx":   {ID: 16777265, VendorID: vendor3GPP},
	"s9":    {ID: 16777267, VendorID: vendor3GPP},
	"s6b":   {ID: 16777272, VendorID: vendor3GPP},
	"slh":   {ID: 16777291, VendorID: vendor3GPP},
	"sy":    {ID: 16777302, VendorID: vendor3GPP},
	"sd":    {ID: 16777303, VendorID: vendor3GPP},
	"sgd":   {ID: 16777313, VendorID: vendor3GPP},
	"gdd":   {ID: 16777313, VendorID: vendor3GPP},
	"np":    {ID: 16777342, VendorID: vendor3GPP},
	"s6t":   {ID: 16777345, VendorID: vendor3GPP},
	"t6a":   {ID: 16777346, VendorID: vendor3GPP},
	"t6b":   {ID: 16777346, VendorID: vendor3GPP},
	"gy":    {ID: 4},
	"ro":    {ID: 4},
	"relay": {ID: 0xffffffff},
}

type options struct {
	target   string
	cfg      peer.Config
	count    int
	interval time.Duration
	json     bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "dping: %v\n", err)
		return exitError
	}
	if opts == nil { // -version
		fmt.Fprintf(stdout, "dping %s, names from %s\n", version, d.IANASource())
		return exitOK
	}
	r := newReporter(stdout, opts)
	code := ping(ctx, opts, r)
	r.finish()
	return code
}

func ping(ctx context.Context, opts *options, r *reporter) int {
	cfg := opts.cfg
	r.start()
	s, took, err := peer.Connect(ctx, cfg)
	if err != nil {
		r.failure(err, errorHint(err, cfg, stageConnect))
		return exitError
	}
	r.connected(s.Local, s.Remote, took)
	caps, err := s.Capabilities()
	if err != nil {
		s.Close()
		r.failure(err, errorHint(err, cfg, stageCapabilities))
		return exitNoAnswer
	}
	r.capabilities(caps, resultHint(caps.ResultCode, cfg))
	if !d.IsSuccess(caps.ResultCode) {
		s.Close() // the peer closes the connection after a failed CEA anyway
		return exitNoAnswer
	}
	answered, open := 0, true
	for seq := 1; opts.count == 0 || seq <= opts.count; seq++ {
		if seq > 1 && !sleep(ctx, opts.interval) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		ans, err := s.Watchdog()
		switch {
		case err == nil:
			r.watchdog(seq, ans)
			if d.IsSuccess(ans.ResultCode) {
				answered++
			}
			continue
		case isTimeout(err):
			r.watchdogTimeout(seq, cfg.Timeout)
			continue
		}
		r.failure(err, errorHint(err, cfg, stageWatchdog))
		open = false
		break
	}
	if open {
		dpa, err := s.Disconnect()
		r.disconnect(dpa, err)
	} else {
		s.Close()
	}
	if answered == 0 && r.sent > 0 {
		return exitNoAnswer
	}
	return exitOK
}

func sleep(ctx context.Context, wait time.Duration) bool {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// parseArgs accepts flags before and after the target, as ping does. It returns nil options for -version.
func parseArgs(args []string, stderr io.Writer) (*options, error) {
	fs := flag.NewFlagSet("dping", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := defaultOriginHost()
	count := fs.Int("c", 4, "number of Device-Watchdog-Requests, 0 to send until interrupted")
	interval := fs.Duration("i", time.Second, "wait between Device-Watchdog-Requests")
	timeout := fs.Duration("W", 2*time.Second, "timeout for the connection and for each answer")
	sctp := fs.Bool("sctp", false, "connect over SCTP instead of TCP (Linux only)")
	originHost := fs.String("origin-host", host, "Origin-Host dping sends")
	originRealm := fs.String("origin-realm", "", "Origin-Realm dping sends (default: the domain of -origin-host)")
	apps := fs.String("app", "relay", "applications to advertise, comma separated: a name listed below or ID[:VENDOR]")
	jsonOut := fs.Bool("json", false, "print one JSON object at the end instead of text")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() { usage(fs) }

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	var target string
	if fs.NArg() > 0 {
		target = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return nil, err
		}
	}
	if *showVersion {
		return nil, nil
	}
	switch {
	case target == "":
		fs.Usage()
		return nil, errors.New("missing host")
	case fs.NArg() > 0:
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case *count < 0:
		return nil, errors.New("-c must be 0 or more")
	case *interval <= 0 || *timeout <= 0:
		return nil, errors.New("-i and -W must be positive")
	case *originHost == "":
		return nil, errors.New("-origin-host must not be empty")
	}
	realm := *originRealm
	if realm == "" {
		_, realm, _ = strings.Cut(*originHost, ".")
		if realm == "" {
			return nil, errors.New("-origin-host has no domain: pass -origin-realm")
		}
	}
	appList, err := parseApps(*apps)
	if err != nil {
		return nil, err
	}
	network := "tcp"
	if *sctp {
		network = "sctp"
	}
	return &options{
		target: target,
		cfg: peer.Config{
			Network:     network,
			Address:     withPort(target),
			OriginHost:  *originHost,
			OriginRealm: realm,
			Apps:        appList,
			Timeout:     *timeout,
		},
		count:    *count,
		interval: *interval,
		json:     *jsonOut,
	}, nil
}

func usage(fs *flag.FlagSet) {
	w := fs.Output()
	fmt.Fprint(w, `usage: dping [flags] host[:port]

Checks a Diameter peer: connects over TCP or SCTP, exchanges capabilities (CER/CEA), sends
Device-Watchdog-Requests and disconnects cleanly (DPR/DPA). The port defaults to 3868.

flags:
`)
	fs.PrintDefaults()
	names := make([]string, 0, len(appAliases))
	for name := range appAliases {
		names = append(names, name)
	}
	slices.Sort(names)
	fmt.Fprintf(w, `
applications for -app: %s

exit status: 0 the peer accepted the CER and answered a DWR, 1 the peer rejected dping or did not
answer, 2 no connection or bad usage
`, strings.Join(names, ", "))
}

// defaultOriginHost is this host's name, made into a domain name when it has no dot.
func defaultOriginHost() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "dping"
	}
	if !strings.Contains(name, ".") {
		name += ".localdomain"
	}
	return name
}

func withPort(target string) string {
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	return net.JoinHostPort(strings.Trim(target, "[]"), defaultPort)
}

func parseApps(list string) ([]peer.App, error) {
	var apps []peer.App
	for _, item := range strings.Split(list, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if app, ok := appAliases[item]; ok {
			apps = append(apps, app)
			continue
		}
		idText, vendorText, hasVendor := strings.Cut(item, ":")
		id, err := strconv.ParseUint(idText, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("-app %q: not a known application name or ID[:VENDOR]", item)
		}
		app := peer.App{ID: uint32(id)}
		if hasVendor {
			vendor, err := strconv.ParseUint(vendorText, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("-app %q: vendor is not a number", item)
			}
			app.VendorID = uint32(vendor)
		}
		apps = append(apps, app)
	}
	return apps, nil
}
