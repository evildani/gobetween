ARG BASE_IMAGE=scratch

# ---------------------  dev (build) image --------------------- #

FROM golang:1.19 as builder


RUN mkdir -p /opt/gobetween
WORKDIR /opt/gobetween

COPY . .

RUN go build .

# --------------------- final image --------------------- #

FROM $BASE_IMAGE

WORKDIR /

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /opt/gobetween/gobetween  .

CMD ["/gobetween", "-c", "/etc/gobetween/conf/gobetween.toml"]

LABEL org.label-schema.vendor="gobetween" \
      org.label-schema.url="http://gobetween.io" \
      org.label-schema.name="gobetween" \
      org.label-schema.description="Modern & minimalistic load balancer for the Сloud era"
