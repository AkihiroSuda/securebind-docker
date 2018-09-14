FROM golang:1.11-alpine AS builder
# FIXME: eliminate gcc
RUN apk add --no-cache build-base
COPY . /go/src/github.com/AkihiroSuda/securebind-docker
WORKDIR /go/src/github.com/AkihiroSuda/securebind-docker
RUN go build -ldflags "-linkmode external -extldflags -static" -a  -o /driver *.go

FROM alpine
RUN apk add --no-cache fuse
RUN mkdir -p /run/docker/plugins /mnt/root /mnt/volumes
COPY --from=builder /driver /driver
CMD ["/driver"]
