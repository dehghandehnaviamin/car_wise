FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/carwise-api ./cmd/api

FROM alpine:3.22
WORKDIR /app
RUN adduser -D app
COPY --from=build /out/carwise-api /app/carwise-api
COPY data /app/data
USER app
EXPOSE 8080
ENTRYPOINT ["/app/carwise-api"]
