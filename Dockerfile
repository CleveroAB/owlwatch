# syntax=docker/dockerfile:1

# ---- Stage 1: frontend ------------------------------------------------------
# Runs on the build host's native platform (fast under emulation-free buildx);
# its output (web/dist) is platform-independent.
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Stage 2: backend -------------------------------------------------------
# Also runs on the build host's platform and cross-compiles to the target:
# Go cross-compilation (CGO_ENABLED=0) is much faster than emulating the
# compiler under QEMU for multi-arch builds.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# web/dist is excluded by .dockerignore; the go:embed directive in web/embed.go
# needs it, so bring in the freshly built UI from stage 1.
COPY --from=frontend /app/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /owlwatch ./cmd/owlwatch

# ---- Stage 3: runtime -------------------------------------------------------
# Distroless with glibc (base, not static): the NVIDIA container toolkit
# injects nvidia-smi and its driver libraries at runtime, and those are
# dynamically linked against glibc. No shell, no package manager.
FROM gcr.io/distroless/base-debian12
COPY --from=builder /owlwatch /owlwatch
ENV OWLWATCH_DB=/data/owlwatch.db
EXPOSE 8080
VOLUME /data
# Exec form only — the image has no shell, so a string-form CMD would fail.
# `owlwatch -healthcheck` GETs http://127.0.0.1:$OWLWATCH_PORT/healthz and
# exits 0/1.
HEALTHCHECK --interval=30s --timeout=5s CMD ["/owlwatch", "-healthcheck"]
ENTRYPOINT ["/owlwatch"]
