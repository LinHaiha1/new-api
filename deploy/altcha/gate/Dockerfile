FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY templates ./templates
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/altcha-gate .

FROM scratch

COPY --from=builder /out/altcha-gate /altcha-gate
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/altcha-gate"]
