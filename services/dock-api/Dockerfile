# dock-api. Build from services/: docker build -f dock-api/Dockerfile services
FROM golang:1.24.7-alpine AS build
WORKDIR /src
COPY core-lib core-lib
COPY dock-api dock-api
WORKDIR /src/dock-api
RUN CGO_ENABLED=0 go build -o /out/dock-api .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/dock-api /app/dock-api
COPY dock-api/config /app/config
ENV APP_SERVER_ADDRESS=:8082
EXPOSE 8082
CMD ["/app/dock-api", "serve"]
