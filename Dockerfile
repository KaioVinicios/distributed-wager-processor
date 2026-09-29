# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/pda ./cmd/pda

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pda /pda
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/pda"]
