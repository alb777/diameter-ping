// Command demo records docs/demo.gif from real dping runs against go-diameter peers on localhost, over TCP
// and SCTP: every line of dping output in the GIF comes from a run made while recording. It needs agg
// (https://github.com/asciinema/agg) on PATH and a kernel with SCTP.
//
// Usage, from the interop directory: go run ./demo
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alb777/diameter-ping/interop"
)

const (
	cols, rows = 104, 20
	prompt     = "\x1b[1;32m$\x1b[0m "
	comment    = "\x1b[2m"
	reset      = "\x1b[0m"
	peerAddr   = "127.0.0.1:3868"
	origin     = "mme.epc.example"
)

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal("demo: ", err)
	}
}

func run() error {
	agg, err := exec.LookPath("agg")
	if err != nil {
		return errors.New("needs agg on PATH: https://github.com/asciinema/agg")
	}
	tmp, err := os.MkdirTemp("", "dping-demo")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "dping")
	build := exec.Command("go", "build", "-o", bin, "./cmd/dping")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %v\n%s", err, out)
	}
	for _, network := range []string{"tcp", "sctp"} {
		ln, _, err := interop.Start(network, peerAddr, "hss.epc.example", "epc.example")
		if err != nil {
			return fmt.Errorf("go-diameter peer over %s: %w", network, err)
		}
		defer ln.Close()
	}

	// Each scene is a command line and the strings its real output must contain, so a run that went wrong
	// is not recorded as if it had worked.
	scenes := []struct {
		note string
		args []string
		want []string
	}{
		{"# an HSS over TCP, advertising S6a", []string{"-c", "3", "-app", "s6a", "-origin-host", origin, "127.0.0.1"},
			[]string{"2001 DIAMETER_SUCCESS", "3 DWA received"}},
		{"# the same peer over SCTP", []string{"-sctp", "-c", "2", "-app", "s6a", "-origin-host", origin, "127.0.0.1"},
			[]string{"over SCTP", "2 DWA received"}},
		{"# when the peer says no, dping says why", []string{"-c", "1", "-app", "16777999:10415", "-origin-host", origin,
			"127.0.0.1"}, []string{"5010 DIAMETER_NO_COMMON_APPLICATION", "hint:"}},
		{"", []string{"-origin-host", origin, "127.0.0.1:3869"}, []string{"connection refused", "hint:"}},
	}
	c := &cast{}
	for i, s := range scenes {
		out, err := exec.Command(bin, s.args...).CombinedOutput()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			return err
		}
		for _, want := range s.want {
			if !strings.Contains(string(out), want) {
				return fmt.Errorf("dping %s: output lacks %q, not recording it:\n%s", strings.Join(s.args, " "), want, out)
			}
		}
		if i > 0 && s.note != "" {
			c.clear(4)
		}
		if s.note != "" {
			c.command(s.note, comment)
		}
		c.command("dping "+strings.Join(s.args, " "), "")
		c.lines(string(out), 0.04)
	}

	castPath := filepath.Join(tmp, "demo.cast")
	if err := c.write(castPath); err != nil {
		return err
	}
	gif := filepath.Join("..", "docs", "demo.gif")
	if err := os.MkdirAll(filepath.Dir(gif), 0o755); err != nil {
		return err
	}
	render := exec.Command(agg, "--theme", "github-dark", "--font-family", "DejaVu Sans Mono", "--font-size", "16",
		"--last-frame-duration", "8", castPath, gif)
	render.Stderr = os.Stderr
	if err := render.Run(); err != nil {
		return fmt.Errorf("agg: %w", err)
	}
	info, err := os.Stat(gif)
	if err != nil {
		return err
	}
	fmt.Printf("docs/demo.gif: %d KiB\n", info.Size()/1024)
	return nil
}

// cast is an asciicast v2 recording built event by event.
type cast struct {
	clock  float64
	events [][]any
}

func (c *cast) out(text string, delay float64) {
	c.clock += delay
	c.events = append(c.events, []any{math.Round(c.clock*1000) / 1000, "o", strings.ReplaceAll(text, "\n", "\r\n")})
}

// command types a command line at a human pace, in style (an SGR sequence, or "").
func (c *cast) command(line, style string) {
	c.out(prompt+style, 0.5)
	for _, r := range line {
		c.out(string(r), 0.04)
	}
	c.out(reset+"\n", 0.4)
}

func (c *cast) lines(text string, step float64) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		c.out(line+"\n", step)
	}
}

func (c *cast) clear(pause float64) {
	c.out("\x1b[2J\x1b[H", pause)
}

func (c *cast) write(path string) error {
	header, err := json.Marshal(map[string]any{
		"version": 2, "width": cols, "height": rows, "env": map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		return err
	}
	out := []string{string(header)}
	for _, e := range c.events {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		out = append(out, string(b))
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}
