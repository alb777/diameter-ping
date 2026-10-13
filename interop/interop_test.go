package interop

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
)

func TestDpingAgainstGoDiameter(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dping")
	build := exec.Command("go", "build", "-o", bin, "./cmd/dping")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cases := []struct {
		name string
		args []string
		code int
		want []string
	}{
		{"relay", nil, 0, []string{
			"CEA from server.test: 2001 DIAMETER_SUCCESS",
			`product "go-diameter"`,
			"DWA seq=2 from server.test: 2001 DIAMETER_SUCCESS",
			"DPA: 2001 DIAMETER_SUCCESS",
		}},
		{"credit control", []string{"-app", "gy"}, 0, []string{
			"CEA from server.test: 2001 DIAMETER_SUCCESS",
			"Diameter Credit Control (4)",
		}},
		{"unknown application", []string{"-app", "99"}, 1, []string{
			"5010 DIAMETER_NO_COMMON_APPLICATION",
			"hint: no application in common",
		}},
	}
	for _, network := range []string{"tcp", "sctp"} {
		t.Run(network, func(t *testing.T) {
			addr, reports := serve(t, network)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					args := append([]string{"-c", "2", "-i", "20ms", "-W", "2s", "-origin-host", "dping.test"}, tc.args...)
					if network == "sctp" {
						args = append(args, "-sctp")
					}
					out, err := exec.Command(bin, append(args, addr)...).CombinedOutput()
					code := 0
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						code = exit.ExitCode()
					} else if err != nil {
						t.Fatal(err)
					}
					if code != tc.code {
						t.Errorf("exit %d, want %d", code, tc.code)
					}
					for _, want := range tc.want {
						if !strings.Contains(string(out), want) {
							t.Errorf("output lacks %q", want)
						}
					}
					if t.Failed() {
						t.Logf("dping %s\n%s", strings.Join(args, " "), out)
					}
					// go-diameter reports messages it could not parse or handle. After a rejected CER it also
					// reports closing the connection, so only a successful exchange must leave no report.
					select {
					case r := <-reports:
						if tc.code == 0 {
							t.Errorf("go-diameter error report: %v", r.Error)
						}
					case <-time.After(50 * time.Millisecond):
					}
				})
			}
		})
	}
}

func serve(t *testing.T, network string) (string, <-chan *diam.ErrorReport) {
	t.Helper()
	ln, reports, err := Start(network, "127.0.0.1:0", "server.test", "test")
	if err != nil {
		if network == "sctp" {
			t.Skipf("no SCTP: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), reports
}
