FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go build -o /backend .

FROM alpine:3.20
COPY --from=build /backend /backend
EXPOSE 9000
ENTRYPOINT ["/backend"]