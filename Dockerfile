FROM golang:1
WORKDIR /src
ENV CGO_ENABLED 0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build
FROM scratch
COPY --from=0 /src/smart-exporter /smart-exporter
COPY drivedb.yaml /
CMD ["/smart-exporter"]