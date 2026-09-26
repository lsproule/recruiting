# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
COPY runner/wire ./runner/wire
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# eclipse-temurin:21.0.8_9-jdk-noble
FROM eclipse-temurin@sha256:03b731a231066bd04ada0a7ee9003cdbc1460083344f9277245b233e5ffcbf47
COPY --from=harness /harness /usr/local/bin/harness
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]
