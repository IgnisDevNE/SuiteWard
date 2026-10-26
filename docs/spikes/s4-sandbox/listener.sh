#!/bin/sh
# Runs inside a container (busybox): answers every TCP connection on 8080 with $BANNER, standing in for a service.
# usage: <runtime> run -d --name <n> -e BANNER=<text> [--network ...] [-p 127.0.0.1:18091:8080] <image> sh -c "$(cat listener.sh)"
printf '#!/bin/sh\nprintf "HTTP/1.0 200 OK\r\n\r\n%s\n"\n' "$BANNER" > /tmp/h.sh
chmod +x /tmp/h.sh
exec nc -lk -p 8080 -e /tmp/h.sh
