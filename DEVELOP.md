
harnessh should be able to be developed within harnessh. You can do this with rootless podman in rootless podman

https://vllm.tailb55c1.ts.net/v1

$ podman run --security-opt label=disable --user podman --device /dev/fuse quay.io/podman/stable podman run alpine echo hello

