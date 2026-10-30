Users wanting more protection than Linux Containers will want to use a virtual machine. We need to test if hsh works with --runtime=krun for the pod.

References:

- https://github.com/containers/crun/issues/2090

## Spike findings

Environment: podman 5.8, crun 1.28 as `krun`, libkrun 1.19, `/dev/kvm` rootless. Run with `CONTAINERS_CONF_OVERRIDE` containing `[engine] runtime="krun"` (equivalent to `--annotation=run.oci.handler=krun`); no hsh code changes.

What works:

- `podman run --annotation=run.oci.handler=krun IMAGE` boots a microVM (guest kernel 6.12.109); a pod with a krun container also starts.
- Inside one krun container with `--cap-add NET_ADMIN,SYS_ADMIN`, the guest kernel supports veth, `unshare -n`, and netlink, so `hsh-workspace` creates `hsh0`/`hsh1` and the inner netns, and the proxy can listen on `10.77.0.1:8080` and serve the inner netns.

What breaks:

1. **Pod layout.** Each krun container is its own VM with its own kernel, so the proxy and workspace containers no longer share a network namespace. `hsh run` fails with the proxy looping on `bind: cannot assign requested address` for `10.77.0.1` (the veth exists only in the workspace VM) and the workspace reporting the proxy never became ready.
2. **Egress isolation is bypassed.** The default krun network is TSI (transparent socket impersonation, `tsi_hijack`, a `dummy0` interface): `socket(AF_INET)` is proxied to the host regardless of the guest network namespace. Inside a krun container, `unshare -n curl https://1.1.1.1` succeeds from a namespace with only `lo`, and from the inner netns of the combined proxy+workspace run `curl --noproxy '*' https://1.1.1.1` returned 301. Under crun the same direct connection fails. The inner netns is therefore not an egress boundary under TSI.
3. In the combined run the proxy's upstream DNS lookup failed (`Temporary failure in name resolution`) while the guest's own `curl` resolved fine; not diagnosed.

Not resolved: `--annotation krun.use_passt=1` (virtio-net via passt, which would disable TSI and restore netns isolation) fails with `krun: failed configuring mounts for handler at phase: HANDLER_CONFIGURE_AFTER_MOUNTS: start passt`, with or without passt installed in the image and with `--network` none/host/default. Passt itself runs fine from the host shell.

Next steps if pursued:

- Find why crun cannot start passt (crun `--debug` gives nothing; needs strace or a crun built with logging), then re-test netns isolation with virtio-net.
- Decide the VM boundary: proxy and workspace in one VM (needs a single-container layout and the proxy secrets/CA inside the guest) or the proxy outside the VM reached over virtio-net/vsock from a workspace-only VM.
- Verify the DNS failure in the combined run.
