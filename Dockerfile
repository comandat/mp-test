# syntax=docker/dockerfile:1.6

FROM golang:1.24-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
