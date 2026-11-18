#!/usr/bin/env python3
"""A logging HTTPS proxy for spike items 3 and 65: which hosts a CLI contacts.

Accepts CONNECT host:port and plain-HTTP requests, logs one line per
connection (time, method, host:port, bytes each way), and tunnels it. It reads
no content: the TLS stays end to end. Run it in a spike sandbox as root, and
point the CLI at it with HTTPS_PROXY=http://127.0.0.1:PORT.

  hosts-proxy.py PORT LOGFILE
"""

import socket
import sys
import threading
import time

PORT, LOG = int(sys.argv[1]), sys.argv[2]
lock = threading.Lock()


def log(line):
    with lock, open(LOG, "a") as f:
        f.write(f"{time.strftime('%H:%M:%S')} {line}\n")


def pipe(src, dst, count):
    try:
        while True:
            b = src.recv(65536)
            if not b:
                break
            count[0] += len(b)
            dst.sendall(b)
    except OSError:
        pass
    finally:
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def handle(c):
    try:
        head = b""
        while b"\r\n\r\n" not in head:
            b = c.recv(4096)
            if not b:
                return
            head += b
        first = head.split(b"\r\n", 1)[0].decode("latin-1")
        method, target = first.split(" ")[:2]
        if method == "CONNECT":
            host, port = target.rsplit(":", 1)
            rest = b""
        else:  # plain HTTP through the proxy: absolute URL
            hp = target.split("://", 1)[1].split("/", 1)[0]
            host, port = (hp.rsplit(":", 1) + ["80"])[:2] if ":" in hp else (hp, "80")
            rest = head
        try:
            u = socket.create_connection((host, int(port)), timeout=15)
        except OSError as e:
            log(f"{method} {host}:{port} FAILED {e}")
            c.sendall(b"HTTP/1.1 502 Bad Gateway\r\n\r\n")
            return
        if method == "CONNECT":
            c.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
        else:
            u.sendall(rest)
        up, down = [0], [0]
        t = threading.Thread(target=pipe, args=(c, u, up))
        t.start()
        pipe(u, c, down)
        t.join()
        log(f"{method} {host}:{port} up={up[0]} down={down[0]}")
    except Exception as e:  # a spike tool: log and go on
        log(f"ERROR {e!r}")
    finally:
        c.close()


s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", PORT))
s.listen(64)
while True:
    conn, _ = s.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
