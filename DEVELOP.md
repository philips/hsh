## Getting Started

Build the project (Go 1.27):

```
git clone https://github.com/philips/hsh
cd hsh
./build.sh
```

`./build.sh` builds `./hsh` the way the release workflow does: it sets
`GOEXPERIMENT=simd` (the terminal emulator needs it), `CGO_ENABLED=0`, and
`GOOS=linux`, and embeds gVisor's runsc for `hsh run --runtime gvisor`. On
first use it downloads the pinned gVisor release for `GOARCH`, verifies it,
and keeps the trimmed payload in `internal/gvisor/.cache/<GOARCH>/` (git
ignored), so later builds and architecture switches are offline.

- `./build.sh --no-gvisor` skips gVisor (about 30 MB smaller, no download);
  `--runtime gvisor` then uses `runsc` from `HSH_RUNSC` or `PATH`.
- `-o PATH` sets the output; other flags go to `go build`
  (`./build.sh -trimpath -ldflags="-s -w"`).
- `GOARCH=arm64 ./build.sh -o hsh-arm64` cross-compiles.
- `PAYLOAD_FORCE=1 ./build.sh` refetches the gVisor payload.

`hsh version` shows whether the terminal emulator and gVisor are built in.
A plain `go build .` also works: without `GOEXPERIMENT=simd` the emulator
(`internal/vt`) is compiled out and its tests are skipped, and without
`-tags gvisor_embed` gVisor is not embedded. See [docs/gvisor.md](docs/gvisor.md).

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

