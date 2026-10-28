# Shape (b): the executor as a container that drives its account's podman through a mounted user socket. podman-remote is the client.
FROM docker.io/library/almalinux:9
RUN dnf -y install podman-remote curl-minimal && dnf clean all
