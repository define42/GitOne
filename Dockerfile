FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /gitone ./cmd/gitone
FROM scratch
COPY --from=build /gitone /gitone
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/gitone","-root","/data","-listen",":8080"]
