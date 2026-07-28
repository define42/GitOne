FROM node:22-alpine AS ui
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci --no-audit --no-fund
COPY web ./web
RUN mkdir -p ./internal/webui/dist && npm --prefix web run build

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist/app.js ./internal/webui/dist/app.js
RUN CGO_ENABLED=0 go test ./... 
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /gitone ./cmd/gitone
FROM scratch
COPY --from=build /gitone /gitone
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/gitone","-root","/data","-listen",":8080"]
