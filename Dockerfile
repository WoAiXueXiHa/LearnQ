FROM golang:1.25-alpine AS build
WORKDIR /src
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGET=api
RUN CGO_ENABLED=0 GOWORK=off go build -trimpath -o /out/app ./cmd/${TARGET}

FROM alpine:3.22
RUN adduser -D -u 10001 learnq \
    && mkdir -p /app/data/reports /app/data/images \
    && chown -R learnq:learnq /app
WORKDIR /app
COPY --from=build /out/app /app/app
USER learnq
ENTRYPOINT ["/app/app"]
