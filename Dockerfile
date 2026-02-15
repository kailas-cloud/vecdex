FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /vecdex ./cmd/vecdex

FROM alpine:3.21
WORKDIR /app
COPY --from=build /vecdex /app/vecdex
COPY config/ /app/config/
EXPOSE 8080
ENTRYPOINT ["/app/vecdex"]
