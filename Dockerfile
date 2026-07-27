FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /puregit ./cmd/puregit
FROM scratch
COPY --from=build /puregit /puregit
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/puregit","-root","/data","-listen",":8080"]
