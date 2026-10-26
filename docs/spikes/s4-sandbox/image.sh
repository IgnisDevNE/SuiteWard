# The images of the spike, sourced by the other scripts: ALPINE is the golang pin of deploy/Containerfile, BOOKWORM the Debian image it was compared
# with (its digest was resolved from the tag on 2026-10-26). The digests are kept apart from the names on purpose: scripts/check-pins.ps1 compares
# every name@digest it finds in the repository, and these are not copies of the Containerfile's line.
ALPINE_DIGEST=f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd
BOOKWORM_DIGEST=972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5
ALPINE=docker.io/library/golang:1.27.3-alpine@sha256:$ALPINE_DIGEST
BOOKWORM=docker.io/library/golang:1.27.3-bookworm@sha256:$BOOKWORM_DIGEST
