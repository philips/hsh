package netsetup

import (
	"fmt"
	"os"

	"github.com/vishvananda/netlink"
)

const (
	HostVethName      = "hsh0"
	InnerVethName     = "hsh1"
	HostIP            = "10.77.0.1"
	InnerIP           = "10.77.0.2"
	ProxyPort         = "8080"
	CallbackRelayPort = "7788"
)

func HostCIDR() string  { return HostIP + "/24" }
func InnerCIDR() string { return InnerIP + "/24" }

func ProxyAddr() string { return "http://" + HostIP + ":" + ProxyPort }

func CallbackRelayAddr() string { return InnerIP + ":" + CallbackRelayPort }

func CreateHostVeth() error {
	for _, name := range []string{InnerVethName, HostVethName} {
		if link, err := netlink.LinkByName(name); err == nil {
			if err := netlink.LinkDel(link); err != nil {
				return fmt.Errorf("delete stale %s: %w", name, err)
			}
		}
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: HostVethName},
		PeerName:  InnerVethName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("create veth pair: %w", err)
	}
	link, err := netlink.LinkByName(HostVethName)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(HostCIDR())
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("assign address to %s: %w", HostVethName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	return nil
}

func MoveInnerVethToPID(pid int) error {
	link, err := netlink.LinkByName(InnerVethName)
	if err != nil {
		return err
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return fmt.Errorf("open child network namespace: %w", err)
	}
	defer ns.Close()
	if err := netlink.LinkSetNsFd(link, int(ns.Fd())); err != nil {
		return fmt.Errorf("move %s to child network namespace: %w", InnerVethName, err)
	}
	return nil
}

func ConfigureInner() error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return err
	}
	link, err := netlink.LinkByName(InnerVethName)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(InnerCIDR())
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("assign address to %s: %w", InnerVethName, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	return nil
}
