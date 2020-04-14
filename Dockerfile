FROM golang:1.14.2-buster AS builder
COPY . /go/src/combinestacks/
WORKDIR /go/src/combinestacks
RUN go build --mod=readonly combinestacks.go

FROM gcr.io/distroless/base-debian10:latest AS run
COPY --from=builder /go/src/combinestacks/combinestacks /combinestacks

# Use a non-root user: slightly more secure (defense in depth)
USER nobody
WORKDIR /
EXPOSE 8080
ENTRYPOINT ["/combinestacks", "--addr=:8080"]
