FROM alpine:edge

RUN apk --update --no-cache add ca-certificates

COPY /telekick /telekick

CMD ["/telekick"]
