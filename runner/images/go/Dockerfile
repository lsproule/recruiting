# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
COPY runner/wire ./runner/wire
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# golang:1.27-bookworm
FROM golang@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
COPY --from=harness /harness /usr/local/bin/harness
# Pre-compile the standard library into a read-only cache: compiling runtime
# from scratch needs more memory than a sandbox gets, and Go tolerates a
# read-only GOCACHE for the candidate package itself.
ENV GOCACHE=/opt/gocache GOFLAGS=-mod=mod GOTOOLCHAIN=local GOPROXY=off
RUN mkdir -p /opt/gocache && go build std && chmod -R a+rX /opt/gocache
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]
