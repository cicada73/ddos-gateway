FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY metrics.go ./
COPY limiter ./limiter
RUN go build -o /gateway .

FROM alpine:3.20
COPY --from=build /gateway /gateway
EXPOSE 8080
ENTRYPOINT ["/gateway"]