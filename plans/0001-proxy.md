# Design decisions

- Proxy configuration is done via config file. Proxy process monitors config file for changes.
- Proxy will run inside the pod with bind mount to configuration

Example of using rootless podman with two containers

```
$ podman run -dt --rm --pod new:mypod nginx
$ podman run -it --rm --pod mypod nginx /bin/sh
# curl http://localhost
podman rulez
```
