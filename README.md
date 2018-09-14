# securebind-docker: recursively read-only bind-mount for Docker

## Motivation

`docker run -v /foo:/bar:ro` is not recursively read-only:

```console
$ mount | grep "on /run "
tmpfs on /run type tmpfs (rw,nosuid,noexec,relatime,size=814396k,mode=755)
$ docker run --rm -v /:/host:ro busybox touch /host/run/compromise
```

## Usage

```console
$ ./build.sh
$ docker plugin create securebind . 
$ docker plugin enable securebind
```

```console
$ docker volume create foo -d securebind -o source=/
$ docker run -it --rm -v foo:/host busybox
```
