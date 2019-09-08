FROM scratch

COPY statsd-rewrite-proxy /

ENTRYPOINT ["/statsd-rewrite-proxy"]