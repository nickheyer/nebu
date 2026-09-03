# Mirrors

`nebu store export --dir DIR` copies stored models into a mirror layout:

```
DIR/index.json                     every repository with its commit and file count
DIR/org/name/index.json            path, size, and sha256 of every file
DIR/org/name/<files>               hard linked from the store when possible
```

A `SOURCE_KIND_MIRROR` source reads that layout back over HTTP from `endpoint`, or from a
local `path`. Without an index it falls back to an S3 style `ListObjectsV2` listing of the
endpoint, so a public bucket that holds the files works too, with digests computed on pull.

Air gapped sites export on a connected host, carry the directory across, and configure a
mirror source on the other side. Everything else, inspect, pull, run, and monitor, behaves
the same.
