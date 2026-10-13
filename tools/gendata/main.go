// Command gendata regenerates internal/diameter/iana.json, the Result-Code and Application ID names dping
// prints, from the IANA AAA Parameters registry.
//
// Usage: go run ./tools/gendata
package main

import (
	"cmp"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	source = "https://www.iana.org/assignments/aaa-parameters/aaa-parameters.xml"
	output = "internal/diameter/iana.json"
)

// The Result-Code registry is split by class; Application IDs is one registry.
var (
	resultRegistries = []string{"aaa-parameters-5", "aaa-parameters-6", "aaa-parameters-7", "aaa-parameters-8", "aaa-parameters-9"}
	appRegistry      = "aaa-parameters-46"
)

type registry struct {
	ID         string     `xml:"id,attr"`
	Updated    string     `xml:"updated"`
	Records    []record   `xml:"record"`
	Registries []registry `xml:"registry"`
}

type record struct {
	Value string `xml:"value"`
	Name  string `xml:"name"`
	Xrefs []struct {
		Type string `xml:"type,attr"`
		Data string `xml:"data,attr"`
		Text string `xml:",chardata"`
	} `xml:"xref"`
}

type entry struct {
	Code uint32 `json:"code"`
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
}

type data struct {
	Source       string  `json:"source"`
	Results      []entry `json:"results"`
	Applications []entry `json:"applications"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gendata:", err)
		os.Exit(1)
	}
}

func run() error {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(source)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", source, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var root registry
	if err := xml.Unmarshal(raw, &root); err != nil {
		return err
	}
	// The Result-Code class registries sit inside the Result-Code registry, so index every level.
	byID := map[string]registry{}
	var index func([]registry)
	index = func(rs []registry) {
		for _, r := range rs {
			byID[r.ID] = r
			index(r.Registries)
		}
	}
	index(root.Registries)
	out := data{Source: fmt.Sprintf("IANA AAA Parameters, registry updated %s", root.Updated)}
	for _, id := range resultRegistries {
		out.Results = append(out.Results, entries(byID[id])...)
	}
	out.Applications = entries(byID[appRegistry])
	// A layout change in the registry shows up as far fewer entries, not as an error.
	if len(out.Results) < 60 || len(out.Applications) < 100 {
		return fmt.Errorf("parsed %d Result-Codes and %d Application IDs, the registry layout changed",
			len(out.Results), len(out.Applications))
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d Result-Codes, %d Application IDs (%s)\n", output, len(out.Results), len(out.Applications), root.Updated)
	return nil
}

// entries keeps the assigned single values of a registry: ranges, Unassigned and Reserved rows are skipped.
func entries(r registry) []entry {
	var out []entry
	for _, rec := range r.Records {
		name := strings.TrimSpace(rec.Name)
		code, err := strconv.ParseUint(strings.TrimSpace(rec.Value), 10, 32)
		if err != nil || name == "" || name == "Unassigned" || strings.HasPrefix(name, "Reserved") {
			continue
		}
		out = append(out, entry{Code: uint32(code), Name: name, Ref: ref(rec)})
	}
	slices.SortFunc(out, func(a, b entry) int { return cmp.Compare(a.Code, b.Code) })
	return out
}

// ref names the specification behind a record: "RFC 6733" or "3GPP TS 29.272". People are not specifications.
func ref(rec record) string {
	for _, x := range rec.Xrefs {
		switch {
		case x.Type == "rfc":
			return "RFC " + strings.TrimPrefix(x.Data, "rfc")
		case x.Type != "person" && strings.TrimSpace(x.Text) != "":
			return strings.TrimSpace(x.Text)
		}
	}
	return ""
}
