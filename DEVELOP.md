## Getting Started

Build the project (Go 1.27; the terminal emulator needs the simd
experiment, set it in the environment or with `go env -w GOEXPERIMENT=simd`):

```
git clone https://github.com/philips/hsh
cd hsh
GOEXPERIMENT=simd CGO_ENABLED=0 go build -o hsh .
```

Without `GOEXPERIMENT=simd` everything still builds and tests pass, but the
emulator (`internal/vt`) is compiled out and its tests are skipped.

To embed gVisor's runsc for `hsh run --runtime gvisor` (as release builds do),
generate the payload for your architecture once, then build with the
`gvisor_embed` tag:

```
bash internal/gvisor/payload.sh
GOEXPERIMENT=simd CGO_ENABLED=0 go build -tags gvisor_embed -o hsh .
```

`hsh version` reports whether gVisor is embedded. Without the tag,
`--runtime gvisor` uses `runsc` from `HSH_RUNSC` or `PATH`. See
[docs/gvisor.md](docs/gvisor.md).

Build the example pi container

```
podman build -t localhost/pi images/pi
```

Stash some credentials into the proxy for hsh to use:

```
hsh secret add mistral-api-key
```

Run the pi container you just built

```
./hsh run localhost/pi
```

Start vibing

```
/login mistral
<type foo for the API key and press enter>
```

## Nested Devlopment

TODO

hsh should be able to be developed within hsh. You can do this with rootless podman in rootless podman

$ podman run --security-opt label=disable --user podman --device /dev/fuse quay.io/podman/stable podman run alpine echo hello

