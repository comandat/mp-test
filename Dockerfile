# syntax=docker/dockerfile:1.6

FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
# go.mod uses a `replace` pointing at ./third_party/go-proton-api, so the
# vendored source has to be present before `go mod download`.
COPY go.mod go.sum* ./
COPY third_party ./third_party
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
