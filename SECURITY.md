# Security Policy

## What devgateway is for

devgateway is a development tool. It is meant to sit in front of services you
run on your own machine, on a network you control, and it is **not built for
production or for exposure to an untrusted network**. It intentionally lets you
tamper with traffic: synthesize responses, fail a fraction of calls, add delay,
drop connections. Anyone who can reach it can do those things to the traffic
passing through it.

## Reporting a vulnerability

Report privately through **GitHub Security Advisories**: on the repository page,
open the **Security** tab, choose **Advisories**, then **Report a
vulnerability**. That opens a draft advisory only you and the maintainers can
see. Please do not open a public issue for a security report.

Useful things to include:

- what an attacker can do, and what access they need to do it;
- the version (`GET /api/status` reports it) or the commit;
- a minimal `gateway.json` and route document that reproduce it;
- the requests to send, and what you get back.

This is a small project maintained in spare time, so there is no guaranteed
response time. You will get an acknowledgement as soon as the report is read,
and the advisory thread is where the fix and the disclosure get discussed.

## Supported versions

Fixes go into `main` and into the next release built from it. Older releases are
not patched.

## Out of scope

- **The admin port has no authentication. That is by design.** The admin port
  (`8081` by default) serves the panel and the REST API that can create routes,
  rewrite responses and read captured traffic, and it is unauthenticated because
  the intended use is local. Bind it to a host you trust and do not expose it;
  the README states this under the assumed limits. Reports that the admin port
  is reachable without credentials will be closed as by design.
- **The traffic port speaks plain HTTP to the client.** No TLS termination is
  offered; the gateway talks HTTP or HTTPS to the upstream.
- **Captured traffic is stored as it arrived**, including headers and bodies.
  Whatever you send through the gateway, including credentials, may be in the
  history and visible in the panel. Point the history at a place you are willing
  to have that in.
- Anything that requires an attacker to already have write access to
  `gateway.json`, the route documents or the machine running the gateway.

If a report turns on one of these but shows real harm beyond it — for example a
path that lets a request through the traffic port reach the admin API — that is
in scope, and worth reporting.
