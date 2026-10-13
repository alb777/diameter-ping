# diameter-ping

`dping` checks a Diameter peer the way `ping` checks a host. It connects over TCP or SCTP, exchanges capabilities (CER/CEA), sends Device-Watchdog-Requests, prints every answer with its round-trip time, and disconnects cleanly (DPR/DPA). When the peer refuses, it says why in Diameter terms: the Result-Code by its IANA name, and what usually causes it.

One static binary, Go standard library only, no Diameter stack to install. Useful when a new peer, DRA or interconnect link does not come up and you need to know whether the problem is the network, the transport, or the Diameter identity and applications.

![dping checking a peer over TCP and over SCTP, then explaining a 5010 and a refused connection](docs/demo.gif)

## Example

These are real runs against [go-diameter](https://github.com/fiorix/go-diameter) on localhost.

```console
$ dping -c 3 -app s6a -origin-host mme.epc.example 127.0.0.1
DPING 127.0.0.1:3868 over TCP as mme.epc.example (realm epc.example)
connected to 127.0.0.1:3868 from 127.0.0.1:54108 in 0.095 ms
CEA from hss.epc.example: 2001 DIAMETER_SUCCESS, 0.114 ms
  realm epc.example, product "go-diameter", vendor 0, host 127.0.0.1
  applications: Diameter base accounting (3), Diameter Credit Control (4),
                3GPP Gx (16777238, vendor 10415), NASREQ (1),
                Diameter Credit Control (4, vendor 10415), 3GPP Rx (16777236, vendor 10415),
                3GPP S6a (16777251, vendor 10415), 3GPP S13 (16777252, vendor 10415),
                3GPP SWx (16777265, vendor 10415)
DWA seq=1 from hss.epc.example: 2001 DIAMETER_SUCCESS, 0.049 ms
DWA seq=2 from hss.epc.example: 2001 DIAMETER_SUCCESS, 0.413 ms
DWA seq=3 from hss.epc.example: 2001 DIAMETER_SUCCESS, 0.289 ms
DPA: 2001 DIAMETER_SUCCESS

--- 127.0.0.1:3868 dping statistics ---
3 DWR sent, 3 DWA received, 0% loss, rtt min/avg/max = 0.049/0.250/0.413 ms

$ dping -c 1 -app 16777999:10415 -origin-host mme.epc.example 127.0.0.1
DPING 127.0.0.1:3868 over TCP as mme.epc.example (realm epc.example)
connected to 127.0.0.1:3868 from 127.0.0.1:54116 in 0.342 ms
CEA from hss.epc.example: 5010 DIAMETER_NO_COMMON_APPLICATION, 0.286 ms
  realm epc.example, product "go-diameter", vendor 0, host 127.0.0.1
  hint: no application in common: pass -app with one the peer serves, for example -app s6a (its CEA
        lists them when it sends any)

$ dping -origin-host mme.epc.example 127.0.0.1:3869
DPING 127.0.0.1:3869 over TCP as mme.epc.example (realm epc.example)
error: dial tcp 127.0.0.1:3869: connect: connection refused
hint: the host answered with a TCP reset: nothing listens on that port, or a firewall rejects the
      connection. Some peers only listen on SCTP: try -sctp
```

## Install

```bash
go install github.com/alb777/diameter-ping/cmd/dping@latest
```

Needs Go 1.22 or newer to build. TCP works everywhere Go runs; SCTP needs Linux with the in-tree `sctp` module, which the kernel loads on first use on most distributions.

## Usage

```
dping [flags] host[:port]
```

| Flag | Default | Meaning |
|---|---|---|
| `-c` | 4 | Device-Watchdog-Requests to send, 0 to send until interrupted |
| `-i` | 1s | wait between DWRs |
| `-W` | 2s | timeout for the connection and for each answer |
| `-sctp` | off | connect over SCTP instead of TCP |
| `-origin-host` | this host's name, plus `.localdomain` when it has no dot | Origin-Host in every request |
| `-origin-realm` | domain of `-origin-host` | Origin-Realm in every request |
| `-app` | `relay` | applications to advertise in the CER, comma separated |
| `-json` | off | one JSON object at the end instead of text |

The port defaults to 3868. Flags may come before or after the host.

`-app` takes interface names, which map to the application ids in the IANA registry: `cx`, `gdd`, `gx`, `gy`, `np`, `relay`, `ro`, `rx`, `s13`, `s6a`, `s6b`, `s6d`, `s6t`, `s9`, `sd`, `sgd`, `sh`, `slg`, `slh`, `swm`, `swx`, `sy`, `t6a`, `t6b`. Anything else is `ID` or `ID:VENDOR`. An application with a vendor, which all the 3GPP names above have (10415), goes into the CER as a Vendor-Specific-Application-Id plus a Supported-Vendor-Id; one without goes in as an Auth-Application-Id. The default `relay` (4294967295) is what agents advertise; a peer that only talks to known clients may answer 5010 to it, so pass the application the peer actually serves.

Exit status, as in `ping`: 0 when the peer accepted the CER and answered at least one DWR, 1 when it rejected dping or did not answer, 2 when there was no connection or the arguments were wrong.

## What a failure tells you

| What dping prints | Usual cause |
|---|---|
| connection refused over TCP | nothing listens on that port, or a firewall sends resets; some peers only listen on SCTP |
| connection refused over SCTP | the host answered with an ABORT: nothing listens on that port over SCTP |
| no answer to the connection attempt | a firewall drops the packets, or the address or port is wrong |
| the peer closed the connection without a CEA | the source address or Origin-Host is not in the peer's list; many peers close silently instead of answering 3010 |
| connected, but no CEA arrived | the port does not speak Diameter, or the peer ignores this source |
| `3010 DIAMETER_UNKNOWN_PEER` | the peer does not accept this Origin-Host, realm or source address |
| `5010 DIAMETER_NO_COMMON_APPLICATION` | none of the advertised applications matches the peer's |
| `5017 DIAMETER_NO_COMMON_SECURITY` | the peer wants TLS or another inband security mechanism |
| `ELECTION_LOST` (4003) | both sides connected at the same time and the peer kept its own connection |

Any `Error-Message` and the AVPs inside `Failed-AVP` are printed under the answer.

## How it is tested

1. The codec is checked against messages worked out by hand from RFC 6733, and against malformed input.
2. A scripted fake peer covers the capabilities exchange, watchdogs, timeouts, a peer that closes after the CER, a peer that sends its own DWR, and the CER contents, over TCP and over SCTP on loopback.
3. The `interop` module runs the real binary against [go-diameter](https://github.com/fiorix/go-diameter), an implementation that shares no code with dping, over TCP and SCTP. It is a separate Go module so dping itself keeps no dependencies.

## Data

Result-Code and application names come from the [IANA AAA Parameters registry](https://www.iana.org/assignments/aaa-parameters/) (updated 2026-01-27), embedded in the binary. `go run ./tools/gendata` regenerates them.

## Not covered yet

TLS and DTLS, SCTP multi-homing (dping uses one address per association), application requests such as a ULR or an AIR, and a responder mode to test a path from the other side.

## Development

```bash
go vet ./... && go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
(cd interop && go test ./...)    # against go-diameter
go run ./tools/gendata           # refresh the IANA names
(cd interop && go run ./demo)    # re-record docs/demo.gif from real runs, needs agg on PATH
```

## License

MIT.
