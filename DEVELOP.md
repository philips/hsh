
harnessh should be able to be developed within harnessh
$ podman run --security-opt label=disable --user podman --device /dev/fuse quay.io/podman/stable podman run alpine echo hello

