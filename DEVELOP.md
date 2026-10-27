## Getting Started

Build the project:

```
git clone https://github.com/philips/hsh
cd hsh
CGO_ENABLED=0 go build -o hsh .
```

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

