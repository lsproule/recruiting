# golang:1.27-alpine
FROM golang@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS harness
WORKDIR /src
COPY go.mod go.sum ./
COPY runner/harness ./runner/harness
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /harness ./runner/harness

# eclipse-temurin:21-jdk-noble
FROM eclipse-temurin@sha256:75ce56643243c3db632be2ef259625fb42ee3be1334389659f7a1a61acb78783
COPY --from=harness /harness /usr/local/bin/harness
ARG KOTLIN_VERSION=2.1.20
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl unzip ca-certificates \
    && curl -fsSL -o /tmp/kotlin.zip "https://github.com/JetBrains/kotlin/releases/download/v${KOTLIN_VERSION}/kotlin-compiler-${KOTLIN_VERSION}.zip" \
    && unzip -q /tmp/kotlin.zip -d /opt && rm /tmp/kotlin.zip \
    && apt-get purge -y curl unzip && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
# The stock kotlinc launcher preloads every compiler jar into one byte array
# and is OOM-killed under the sandbox memory cap; calling the compiler class
# directly fits, and -Xmx keeps the JVM inside the cgroup either way.
RUN printf '%s\n' '#!/bin/sh' \
    'exec java -Xmx192m -Xss8m -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -XX:SharedArchiveFile=/opt/kotlinc.jsa -Xshare:auto -cp /opt/kotlinc/lib/kotlin-compiler.jar org.jetbrains.kotlin.cli.jvm.K2JVMCompiler "$@"' \
    > /usr/local/bin/kotlinc && chmod 0755 /usr/local/bin/kotlinc
# Class-data archive of a real compile: loading the compiler's classes from
# the jars is most of a cold kotlinc, and the sandbox cannot cache anything.
RUN mkdir -p /opt/warm && cd /opt/warm \
    && printf '%s\n' 'fun main() { println("warm") }' > warm.kt \
    && java -Xmx320m -XX:ArchiveClassesAtExit=/opt/kotlinc.jsa \
       -cp /opt/kotlinc/lib/kotlin-compiler.jar org.jetbrains.kotlin.cli.jvm.K2JVMCompiler \
       warm.kt -include-runtime -d /opt/warm/warm.jar \
    && chmod -R a+rX /opt/kotlinc.jsa /opt/kotlinc && rm -rf /opt/warm
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/harness"]
