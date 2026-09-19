#!/usr/bin/env python3
"""Test the daemon, embedded UI, and API authentication."""
import argparse
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def probe(base):
    def request(path, token=None, expected=200):
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        try:
            response = urllib.request.urlopen(urllib.request.Request(base + path, headers=headers), timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            assert response.status == expected, (path, response.status, expected)
            return response.read().decode()

    for attempt in range(150):
        try:
            request("/health")
            break
        except (OSError, AssertionError):
            if attempt == 149:
                raise
            time.sleep(0.2)
    html = request("/")
    assert "<html" in html.lower(), "embedded UI missing"
    asset = re.search(r'_app/immutable/entry/start\.[\w-]+\.js', html)
    assert asset, "UI entrypoint missing"
    assert "<html" not in request("/" + asset.group()).lower(), "UI asset fell back to HTML"
    assert "<html" in request("/runtimes").lower(), "SPA routes unavailable"
    assert request("/openapi.yaml").startswith("openapi:"), "embedded API specification missing"
    request("/v1/models", expected=401)
    assert isinstance(json.loads(request("/v1/models", "nebu-smoke-key"))["data"], list)
    print("daemon, embedded UI, SPA, static asset, OpenAPI, and gateway authentication: OK")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", nargs="?")
    parser.add_argument("--url")
    args = parser.parse_args()
    if args.url:
        probe(args.url)
        return
    binary = str(Path(args.binary).resolve())
    with tempfile.TemporaryDirectory(prefix="nebu-smoke-") as directory:
        root = Path(directory)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        config = root / "config.json"
        config.write_text(json.dumps({"data_dir": str(root / "data"), "cache_dir": str(root / "cache")}))
        env = dict(os.environ, NEBU_DATA_DIR=str(root / "data"), NEBU_LISTEN=f"127.0.0.1:{port}",
                   NEBU_TOKEN="nebu-smoke-token", NEBU_API_KEYS="nebu-smoke-key", NEBU_ADDR="")
        version = subprocess.check_output([binary, "-config", str(config), "version"], env=env, text=True)
        assert version.startswith("nebu "), version
        with (root / "daemon.log").open("w+") as log:
            daemon = subprocess.Popen([binary, "-config", str(config), "serve"], env=env, stdout=log, stderr=log)
            try:
                probe(f"http://127.0.0.1:{port}")
                assert (root / "data/nebu.db").is_file(), "database not persisted under data_dir"
            except BaseException:
                log.seek(0)
                print(log.read())
                raise
            finally:
                daemon.terminate()
                try:
                    daemon.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    daemon.kill()
                    daemon.wait()
                    raise
            if os.name != "nt":
                assert daemon.returncode == 0, f"unclean daemon shutdown: {daemon.returncode}"


if __name__ == "__main__":
    main()
