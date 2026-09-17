FROM docker.io/library/golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS build

ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/tarlink ./cmd/tarlink

FROM docker.io/nginxinc/nginx-unprivileged:1.29.1-alpine@sha256:6742337773a7ca839e7a76f489cc684cc410e3de46c0c83be8bb2bb9944360ef

LABEL org.opencontainers.image.source="https://github.com/drobilica/tarlink" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="TarLink static artifact repository"

COPY --from=build /out/tarlink /usr/local/bin/tarlink
COPY build/artifact-repository/nginx.conf /etc/nginx/nginx.conf

ENV HOME=/tmp/tarlink-home \
    XDG_CONFIG_HOME=/tmp/tarlink-home/config \
    XDG_DATA_HOME=/tmp/tarlink-home/data \
    XDG_STATE_HOME=/tmp/tarlink-home/state \
    XDG_CACHE_HOME=/tmp/tarlink-home/cache

EXPOSE 8080
