# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep Python native qualification traffic on loopback or Unix sockets."""

import ipaddress
import socket


def _local(host):
    if isinstance(host, bytes):
        host = host.decode("ascii", errors="replace")
    if host in (None, "", "localhost"):
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _address(address):
    if isinstance(address, tuple) and address and not _local(address[0]):
        raise OSError("external network disabled in native qualification")


def install():
    if getattr(socket, "_cetirun_native_loopback_only", False):
        return
    socket._cetirun_native_loopback_only = True
    connect, connect_ex, getaddrinfo, sendto = socket.socket.connect, socket.socket.connect_ex, socket.getaddrinfo, socket.socket.sendto

    def checked_connect(self, address):
        _address(address)
        return connect(self, address)

    def checked_connect_ex(self, address):
        _address(address)
        return connect_ex(self, address)

    def checked_getaddrinfo(host, *args, **kwargs):
        if not _local(host):
            raise OSError("external DNS disabled in native qualification")
        return getaddrinfo(host, *args, **kwargs)

    def checked_sendto(self, data, *args):
        _address(args[-1])
        return sendto(self, data, *args)

    socket.socket.connect = checked_connect
    socket.socket.connect_ex = checked_connect_ex
    socket.getaddrinfo = checked_getaddrinfo
    socket.socket.sendto = checked_sendto
