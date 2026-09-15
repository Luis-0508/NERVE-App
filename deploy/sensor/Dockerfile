# Official ntop distribution. Validate upgrades with the capture acceptance test.
FROM ntop/ntopng@sha256:4d683a538a2e6b537600aae5c24f0667100ef4c13568e159f06dcea793b0b594
USER root
COPY entrypoint.sh run.sh healthcheck.sh /opt/nerve/
# Windows checkouts may use CRLF; executable bits also do not survive every transfer.
RUN sed -i 's/\r$//' /opt/nerve/*.sh && chmod 755 /opt/nerve/*.sh \
    && command -v redis-server && command -v redis-cli && command -v curl
ENTRYPOINT ["/opt/nerve/entrypoint.sh"]
