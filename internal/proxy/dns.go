package proxy

import (
	"log"
	"net"
	"os"
	"strings"
	"time"

	"hsh/internal/denieddb"

	"github.com/miekg/dns"
)

func readResolvers(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			server := fields[1]
			if _, _, err := net.SplitHostPort(server); err != nil {
				server = net.JoinHostPort(server, "53")
			}
			servers = append(servers, server)
		}
	}
	return servers
}

func (p *Proxy) startDNS(addr string) {
	servers := readResolvers("/etc/resolv.conf")
	if len(servers) == 0 {
		log.Printf("dns: no upstream resolvers found in /etc/resolv.conf; DNS forwarding disabled")
		return
	}
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		p.serveDNS(w, r, servers)
	})
	for _, net_ := range []string{"udp", "tcp"} {
		server := &dns.Server{Addr: addr, Net: net_, Handler: handler}
		go func(s *dns.Server, n string) {
			if err := s.ListenAndServe(); err != nil {
				log.Printf("dns: %s listener on %s: %v", n, addr, err)
			}
		}(server, net_)
	}
	log.Printf("dns: forwarding allowlisted queries on %s via %s", addr, strings.Join(servers, ", "))
}

func (p *Proxy) serveDNS(w dns.ResponseWriter, r *dns.Msg, upstream []string) {
	if len(r.Question) == 0 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}
	q := r.Question[0]
	name := strings.TrimSuffix(q.Name, ".")
	mode := p.mode()
	if mode == "none" {
		log.Printf("dns: refused %s (network mode none)", name)
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}
	if mode == "allowlist" && !p.allowListed(name) {
		log.Printf("dns: denied %s (not in network.allow)", name)
		if p.deniedDB != nil {
			if err := denieddb.RecordDNS(p.deniedDB, name, name+":53"); err != nil {
				log.Printf("recording dns denial of %s: %v", name, err)
			}
		}
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		_ = w.WriteMsg(m)
		return
	}
	var lastErr error
	for _, server := range upstream {
		resp, err := exchangeDNS(r, server, "udp")
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Truncated {
			resp, err = exchangeDNS(r, server, "tcp")
			if err != nil {
				lastErr = err
				continue
			}
		}
		_ = w.WriteMsg(resp)
		return
	}
	log.Printf("dns: upstream failure for %s: %v", name, lastErr)
	m := new(dns.Msg)
	m.SetRcode(r, dns.RcodeServerFailure)
	_ = w.WriteMsg(m)
}

func exchangeDNS(r *dns.Msg, server, network string) (*dns.Msg, error) {
	client := &dns.Client{
		Net:     network,
		Timeout: 5 * time.Second,
	}
	resp, _, err := client.Exchange(r, server)
	return resp, err
}
