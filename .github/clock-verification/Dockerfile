# syntax=docker/dockerfile:1.7
FROM golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c
ENV GOTOOLCHAIN=local CGO_ENABLED=0
COPY wallstep_runtime.go /usr/local/go/src/runtime/timestub.go
WORKDIR /src
COPY --from=k6src / /src/
COPY probe_main.go /src/clockprobe/main.go
RUN go build -buildvcs=false -o /probe/k6-probe ./clockprobe
ENTRYPOINT ["/probe/k6-probe"]
