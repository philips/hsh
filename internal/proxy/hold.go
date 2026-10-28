package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"hsh/internal/attend"
	"hsh/internal/denieddb"
)

const (
	holdPoll = 250 * time.Millisecond

	dnsHoldLimit  = 9 * time.Second
	denialMessage = "hsh: egress to %s is blocked by the sandbox policy and needs the user's approval.\n" +
		"The user can approve it with `hsh monitor`. Do not look for workarounds or alternative hosts;\n" +
		"retry this request later, or tell the user which host is needed.\n"
)

var maxHolds int64 = 64

type holdState struct {
	active atomic.Int64
}

func (p *Proxy) approvalWait() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st.approvalWait
}

func (p *Proxy) holdsEnabled() bool {
	if p.approvalWait() <= 0 || p.mode() == "none" {
		return false
	}
	return attend.Fresh(p.opts.AttendedPath, attend.MaxAge)
}

func (p *Proxy) acquireHold() bool {
	if p.holds.active.Add(1) > maxHolds {
		p.holds.active.Add(-1)
		return false
	}
	return true
}

func (p *Proxy) releaseHold() { p.holds.active.Add(-1) }

func (p *Proxy) recordDenial(host, target string) {
	if p.deniedDB == nil {
		return
	}
	if err := denieddb.Record(p.deniedDB, p.opts.Workspace, p.opts.Session, host, target); err != nil {
		log.Printf("recording denial of %s: %v", host, err)
	}
}

func (p *Proxy) ignoredByUser(host string) bool {
	if p.deniedDB == nil {
		return false
	}
	d, err := denieddb.LookupDecision(p.deniedDB, p.opts.Session, host)
	return err == nil && d.Found && d.Ignored
}

func (p *Proxy) awaitApproval(ctx context.Context, host, target, kind string, allowed func() bool, maxWait time.Duration) bool {
	p.recordDenial(host, target)
	if p.ignoredByUser(host) {
		log.Printf("%s %s: denied (ignored by the user)", kind, host)
		return false
	}
	if !p.acquireHold() {
		log.Printf("%s %s: denied (too many held requests)", kind, host)
		return false
	}
	defer p.releaseHold()
	if p.deniedDB != nil {
		_ = denieddb.AdjustHolding(p.deniedDB, p.opts.Session, host, 1)
		defer func() { _ = denieddb.AdjustHolding(p.deniedDB, p.opts.Session, host, -1) }()
	}
	log.Printf("%s %s: holding for approval (up to %s)", kind, host, maxWait)
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	tick := time.NewTicker(holdPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("%s %s: client gave up while held", kind, host)
			return false
		case <-deadline.C:
			log.Printf("%s %s: not approved within %s", kind, host, maxWait)
			return false
		case <-tick.C:
			if allowed() {
				log.Printf("%s %s: approved while held", kind, host)
				return true
			}
			if p.ignoredByUser(host) {
				log.Printf("%s %s: denied while held (ignored by the user)", kind, host)
				return false
			}
			if !p.holdsEnabled() {
				log.Printf("%s %s: hold ended (holding turned off or nobody is watching)", kind, host)
				return false
			}
		}
	}
}

func (p *Proxy) gate(w http.ResponseWriter, ctx context.Context, host, target, kind string, allowed func() bool) bool {
	if allowed() {
		return true
	}
	if !p.holdsEnabled() {
		p.deny(w, host, target)
		return false
	}
	if p.awaitApproval(ctx, host, target, kind, allowed, p.approvalWait()) {
		return true
	}
	p.writeDeny(w, host)
	return false
}

func (p *Proxy) deny(w http.ResponseWriter, host, target string) {
	log.Printf("denied %s (not in network.allow)", host)
	p.recordDenial(host, target)
	p.writeDeny(w, host)
}

func (p *Proxy) writeDeny(w http.ResponseWriter, host string) {
	w.Header().Set("Proxy-Authenticate", `Hsh realm="egress-allowlist"`)
	http.Error(w, fmt.Sprintf(denialMessage, host), http.StatusProxyAuthRequired)
}
