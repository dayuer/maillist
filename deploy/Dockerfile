# The build stage carries the toolchain and the module cache; the runtime stage carries one static
# binary. CGO_ENABLED=0 is why modernc.org/sqlite was chosen over mattn/go-sqlite3 (design §4.1):
# the pure-Go driver builds this image, the cgo one cannot.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# -buildid= drops the Go build id: it is build-machine noise, and at this size every kilobyte of
# reproducible output is worth having.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
        -o /out/nibwire ./cmd/nibwire \
 && mkdir -p /out/data

# distroless: no shell, no package manager, one nonroot user. The binary is on PATH, so
# `docker compose exec nibwire nibwire contact list` works.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/nibwire /usr/local/bin/nibwire
# /data has to exist and belong to the user the process runs as. Docker creates a mount point that
# does not exist in the image as root, and a fresh named volume copies the ownership of the
# directory it mounts over — so without this the app cannot open its database: it runs as nonroot
# and /data is root's.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV NIBWIRE_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
# The CLI is the healthcheck: it is the same binary, and `health` is one query against the database.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["nibwire", "health"]
ENTRYPOINT ["nibwire"]
CMD ["serve"]
