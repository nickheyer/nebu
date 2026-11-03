# conformance

Checks a gateway and scheduler implementation against the nebu routing protocol and writes a YAML and Markdown report per implementation and version.

## Install

```sh
git clone https://github.com/nickheyer/nebu && cd nebu
```

## Usage

```sh
make conformance    # builds nebu, runs the suite against a fresh daemon, writes conformance/reports/nebu-<version>.{yaml,md}

go test -tags conformance ./conformance -args \
  -gateway-url=https://127.0.0.1:8080 \
  -api-url=https://127.0.0.1:8080 \
  -token=<api token> \
  -tls-ca=<ca.pem> \
  -report=conformance/reports \
  -stub-host=127.0.0.1
```
