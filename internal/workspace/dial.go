package workspace

import (
	"net"
	"strconv"
	"time"
)

const dialTimeout = 3 * time.Second

func parsePort(s string) (int, bool) {
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func DialLocalPort(port int) (net.Conn, error) {
	return dialPort(port, localHosts()...)
}

func localHosts() []string {
	hosts := []string{"127.0.0.1", "::1"}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return hosts
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsUnspecified() {
			continue
		}
		hosts = append(hosts, ipn.IP.String())
	}
	return hosts
}

func dialPort(port int, hosts ...string) (net.Conn, error) {
	p := strconv.Itoa(port)
	var lastErr error
	for _, host := range hosts {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, p), dialTimeout)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
