FROM gcr.io/distroless/static
MAINTAINER Daniel Randall <danny_randall@byu.edu>

COPY crestron-telnet-microservice-linux-amd64 /crestron-telnet-microservice

ENTRYPOINT ["/crestron-telnet-microservice"]
