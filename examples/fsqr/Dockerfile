FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /fsqr ./examples/fsqr/
RUN CGO_ENABLED=0 go build -o /fsqr-loader ./examples/fsqr/loader/

FROM alpine:3.21
COPY --from=build /fsqr /usr/local/bin/fsqr
COPY --from=build /fsqr-loader /usr/local/bin/fsqr-loader
COPY --from=build /src/tests/testdata/paphos_places.jsonl /data/paphos_places.jsonl
EXPOSE 8080
ENTRYPOINT ["fsqr"]
