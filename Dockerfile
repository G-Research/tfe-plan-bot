FROM alpine

RUN apk add mailcap ca-certificates

FROM scratch

STOPSIGNAL SIGINT

VOLUME /tmp

# add required system files
COPY --from=0 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=0 /etc/mime.types /etc/

# add the default configuration file
COPY config/tfe-plan-bot.example.yml /secrets/tfe-plan-bot.yml

# add application file
COPY tfe-plan-bot /

ENTRYPOINT ["/tfe-plan-bot"]
CMD ["server", "--config", "/secrets/tfe-plan-bot.yml"]
