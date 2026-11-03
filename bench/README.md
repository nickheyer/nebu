# nebu-bench

Records, synthesizes and replays gateway traces, reporting p50, p95 and p99 TTFT, TPOT and end to end latency, throughput, errors and shed requests per scheduler configuration.

## Install

```sh
go install github.com/nickheyer/nebu/cmd/nebu-bench@latest
```

## Usage

```sh
nebu-bench synthesize --requests 200 --rate 10 --out t.jsonl
nebu-bench record --api-url https://127.0.0.1:8080 --token-file api.token --tls-ca tls.crt --out t.jsonl
nebu-bench local --gateway-url https://127.0.0.1:8080 --token-file api.token --tls-ca tls.crt --trace t.jsonl --stubs 4 \
  --scheduler-config default --scheduler-config default-nometrics --out runs.json
nebu-bench replay --gateway-url https://127.0.0.1:8080 --token-file api.token --tls-ca tls.crt --trace t.jsonl \
  --pool my-pool --scheduler-config default --scheduler-config default-nometrics --out runs.json
nebu-bench compare runs.json other.json
```
