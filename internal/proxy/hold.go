package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"hsh/internal/attend"
	"hsh/internal/monitordb"
)

const holdPoll = 250 * time.Millisecond

var denialMessage = "hsh: egress to %s is blocked on purpose by the sandbox policy; this is not an outage.\n" +
	"The user has been notified and can approve it with `hsh monitor`.\n" +
	"Tell the user which host you need and why. Do not try other tools, mirrors, proxies, or DNS tricks.\n"

var ruleDenialMessage = "hsh: egress to %s is denied by the user's policy for this session.\n" +
	"Do not ask for it again or try other tools, mirrors, proxies, or DNS tricks.\n"

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
	if p.denials == nil {
		return
	}
	if err := p.denials.Record(host, target); err != nil {
		log.Printf("recording denial of %s: %v", host, err)
	}
}

func (p *Proxy) ruleDenial(host string) (monitordb.Rule, bool) {
	m := p.match(host)
	return m.Rule, m.Access == monitordb.AccessDeny
}

func (p *Proxy) admit(ctx context.Context, rec *connRecord, host, target, kind string, allowed func() bool) bool {
	if allowed() {
		rec.decided(monitordb.AccessAllow, monitordb.LogRecord, p.match(host).Rule)
		return true
	}
	p.recordDenial(host, target)
	m := p.match(host)
	switch {
	case m.Access == monitordb.AccessDeny:
		rec.decided(monitordb.AccessDeny, monitordb.LogRecord, m.Rule)
		rec.write()
		log.Printf("%s %s: denied (%s)", kind, host, m.Rule.DeniedBy())
		return false
	case m.Log == monitordb.LogRecord:
		rec.decided(monitordb.AccessDeny, monitordb.LogRecord, monitordb.Rule{})
		rec.write()
		log.Printf("%s %s: denied (not allowed by the session's rules; recorded, not queued for review)", kind, host)
		return false
	}
	rec.decided(monitordb.AccessDeny, monitordb.LogReview, monitordb.Rule{})
	rec.write()
	if !p.holdsEnabled() {
		log.Printf("%s %s: denied (not allowed by the session's rules)", kind, host)
		return false
	}
	start := time.Now()
	ok := p.awaitApproval(ctx, host, kind, allowed, p.approvalWait())
	rec.held(time.Since(start))
	if ok {
		rec.decided(monitordb.AccessAllow, monitordb.LogReview, p.match(host).Rule)
	}
	rec.write()
	return ok
}

func (p *Proxy) awaitApproval(ctx context.Context, host, kind string, allowed func() bool, maxWait time.Duration) bool {
	if !p.acquireHold() {
		log.Printf("%s %s: denied (too many held requests)", kind, host)
		return false
	}
	defer p.releaseHold()
	if p.denials != nil {
		_ = p.denials.AdjustHolding(host, 1)
		defer func() { _ = p.denials.AdjustHolding(host, -1) }()
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
			if r, denied := p.ruleDenial(host); denied {
				log.Printf("%s %s: denied while held (%s)", kind, host, r.DeniedBy())
				return false
			}
			if !p.holdsEnabled() {
				log.Printf("%s %s: hold ended (holding turned off or nobody is watching)", kind, host)
				return false
			}
		}
	}
}

func (p *Proxy) gate(w http.ResponseWriter, ctx context.Context, rec *connRecord, host, target, kind string, allowed func() bool) bool {
	if p.admit(ctx, rec, host, target, kind, allowed) {
		return true
	}
	p.writeDeny(w, host)
	return false
}

func (p *Proxy) denialText(host string) string {
	if _, denied := p.ruleDenial(host); denied {
		return fmt.Sprintf(ruleDenialMessage, host)
	}
	return fmt.Sprintf(denialMessage, host)
}

func (p *Proxy) writeDeny(w http.ResponseWriter, host string) {
	http.Error(w, p.denialText(host), http.StatusForbidden)
}
