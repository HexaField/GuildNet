# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/hostapp ./cmd/hostapp

# ---- Final stage ----
FROM gcr.io/distroless/static-debian12:nonroot
USER nonroot:nonroot
WORKDIR /
COPY --from=build /out/hostapp /usr/local/bin/hostapp
# App reads/writes ~/.guildnet inside the container; mount a volume if desired.
ENTRYPOINT ["/usr/local/bin/hostapp", "serve"]
