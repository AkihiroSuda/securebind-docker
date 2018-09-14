#!/bin/sh
set -e -x
cd $(dirname $0)
docker build -t securebind-rootfs .
rm -rf rootfs
mkdir rootfs
docker create --name securebind-rootfs securebind-rootfs
docker export securebind-rootfs | tar Cxv ./rootfs
docker rm -f securebind-rootfs
set +x

echo "please run: docker plugin rm -f securebind; docker plugin create securebind . && docker plugin enable securebind"
