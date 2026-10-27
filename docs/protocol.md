# Protocol notes

This document describes the wire format spoken by `tjsocks`. It was derived by
observing the Open TJ client (<https://github.com/sitdownkevin/open_tj>, MIT).
The gateway is a Sangfor-style SSL VPN appliance.

## 1. Login

Two sequential HTTPS requests over TLS 1.2 (`vpn.tongji.cn:443`). The server
only offers legacy cipher suites, so the client pins:

```
TLS_RSA_WITH_AES_128_CBC_SHA
TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
```

Every request carries `Cookie: ANStandalone=true; <session cookies>` and
`appid: SSPVPN`.

```
POST /prx/000/http/localhost/login      (form encoded)
     method=LDAP3 & uname=<user> & pwd=<password>
     & pwd1= & pwd2= & deviceid= & device_name=tjsocks & hardwareid=
     -> 302 Location: .../welcome   (success; cookies are the session)
     -> anything else               (authentication failure)
```

## 2. Tunnel upgrade

```
GET /prx/000/http/localhost/vpntunnel
    x-devtype: 3
    appid: SSPVPN
    clientid: <32 hex chars>
    cpuid: tjsocks
    hostname: tjsocks
    PAYLOAD-IP-VERSION: 6

<- HTTP/1.1 201 ...
   <blank line>
```

After the `201` response the socket stops being HTTP and becomes a raw binary
stream:

1. Client sends a 16-byte request with kind `0x50`, length 0 → gateway replies
   with a 16-byte header (kind `0x51`, little-endian length at offset 12)
   followed by a JSON "common" block (e.g. `keepalive_interval`).
2. Client sends kind `0x54` with a 32-byte body → gateway replies kind `0x55`
   with a JSON "network" block: `client_ipv4`, `dns_servers`, `routes`, ...

## 3. Data plane

The stream then carries IPv4 packets back to back, each framed by the total
length field of its own IP header (`bytes 2..4`, big endian). The tunnel MTU is
1280.

Control messages are IPv4 headers with **protocol number 255**:

```
offset 0    : 0x45
offset 2    : 0x0014 (20, header length)
offset 9    : 0xFF   (protocol 255 = control)
offset 12   : command (uint16)
offset 16   : parameter (uint32)
```

| Command | Direction | Meaning | Reply |
| --- | --- | --- | --- |
| 1 | both | heartbeat | `ATP(1, 0)` when the gateway asks |
| 2 | gateway → client | session terminated; parameter = reason | none, reconnect |
| 4 | gateway → client | liveness probe | `ATP(5, 0)` |

The client also sends `ATP(1, 0)` every `keepalive_interval` seconds.

Observed termination reasons: `1` = session preempted by a newer login,
`8` = unknown/adjacent failure. A single account may only hold one session.

## 4. Logout

```
GET /prx/000/http/localhost/logout   (with the session cookies)
```

## 5. What this project adds

Nothing on the wire. The difference is on the local side: instead of creating a
`utun` device and rewriting host routes (which needs root and takes over the
whole machine), packets are injected into a user-space TCP/IP stack
(gVisor netstack) and exposed through a SOCKS5 server, including UDP.
