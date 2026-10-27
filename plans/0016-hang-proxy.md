# Hang the connection instead of denying

> **Status: will not implement.** Denials stay immediate (HTTP 407 for
> egress, DNS REFUSED for lookups): an answered denial is debuggable
> (`hsh proxy-denied` reviews exactly what was refused, and tools see a
> proxy they understand), while a hang turns every denied request into
> an opaque multi-minute stall that is indistinguishable from a network
> fault.

Instead of denying proxy requests immediately hang the connection for 
