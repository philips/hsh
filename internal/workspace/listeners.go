package workspace

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

type Listener struct {
	Port int
	Addr string
}

func InnerListeners() ([]Listener, error) {
	var all []Listener
	for _, src := range []struct {
		path string
		v6   bool
	}{{"/proc/net/tcp", false}, {"/proc/net/tcp6", true}} {
		f, err := os.Open(src.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		ls, err := ParseListeners(f, src.v6)
		f.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, ls...)
	}
	return dedupeListeners(all), nil
}

func dedupeListeners(in []Listener) []Listener {
	seen := map[int]bool{}
	var out []Listener
	for _, l := range in {
		if seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func ParseListeners(r io.Reader, v6 bool) ([]Listener, error) {
	var out []Listener
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		addr, port, err := parseSockAddr(fields[1], v6)
		if err != nil {
			return nil, err
		}
		out = append(out, Listener{Port: port, Addr: addr.String()})
	}
	return out, sc.Err()
}

func parseSockAddr(s string, v6 bool) (net.IP, int, error) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("malformed socket address %q", s)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return nil, 0, fmt.Errorf("malformed port in %q: %w", s, err)
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return nil, 0, fmt.Errorf("malformed address in %q: %w", s, err)
	}
	want := 4
	if v6 {
		want = 16
	}
	if len(raw) != want {
		return nil, 0, fmt.Errorf("malformed address length in %q", s)
	}
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(raw), int(port), nil
}
