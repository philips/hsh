
harnessh should be able to be developed within harnessh. You can do this with rootless podman in rootless podman

$ podman run --security-opt label=disable --user podman --device /dev/fuse quay.io/podman/stable podman run alpine echo hello

