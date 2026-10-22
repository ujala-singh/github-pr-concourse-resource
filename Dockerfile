FROM golang:1.27.3-alpine AS builder

RUN apk add --no-cache git openssh-client ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /assets/check ./cmd/check && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /assets/in ./cmd/in && \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /assets/out ./cmd/out

FROM alpine:3.24

# git: clone/merge/rebase PR checkouts; openssh-client: SSH remotes;
# git-lfs: LFS checkouts; bash: for `fly intercept` into the container.
# The minimum CA bundle distrusts the compromised eMudhra certificate chains.
RUN apk add --no-cache git openssh-client 'ca-certificates>=20261020-r0' git-lfs bash

COPY --from=builder /assets /opt/resource/

RUN chmod +x /opt/resource/check /opt/resource/in /opt/resource/out

# Add resource metadata
LABEL org.opencontainers.image.title="GitHub PR Concourse Resource"
LABEL org.opencontainers.image.description="A Concourse resource for GitHub Pull Requests with dual-mode support"
LABEL org.opencontainers.image.source="https://github.com/ujala-singh/github-pr-concourse-resource"
