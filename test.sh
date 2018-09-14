#!/bin/bash
set -x

cd $(dirname $0)
docker volume rm -f foo
docker plugin disable securebind
docker plugin rm securebind
docker volume rm -f foo

set -e
./build.sh

docker plugin create securebind .
docker plugin enable securebind
docker volume create foo -d securebind -o source=/

function test::main(){
    docker volume ls
    docker volume inspect foo
    docker run -it --rm -v foo:/mnt busybox ls /mnt
    docker run -it --rm -v foo:/mnt busybox sh -c 'touch /mnt/HACK ; test $? = 1'
}

test::main
sudo systemctl stop docker
sudo systemctl start docker
test::main
