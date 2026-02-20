FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /fsqr-loader ./examples/fsqr/loader

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /fsqr-loader /app/fsqr-loader
ENTRYPOINT ["/app/fsqr-loader"]
