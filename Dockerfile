FROM golang:1.17.2-bullseye AS builder
COPY . /go/src/combinestacks/
WORKDIR /go/src/combinestacks
RUN go build --mod=readonly combinestacks.go

FROM gcr.io/distroless/base-debian11:nonroot-amd64 AS run
COPY --from=builder /go/src/combinestacks/combinestacks /combinestacks

# Use a non-root user: slightly more secure (defense in depth)
USER nonroot
WORKDIR /
EXPOSE 8080
ENTRYPOINT ["/combinestacks", "--addr=:8080"]
