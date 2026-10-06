#!/usr/bin/env bash
# Disposable local OpenBao only; never reads production credentials.
set -euo pipefail
set +x
umask 077
cd "$(dirname "$0")/.."
test_dir=$(mktemp -d)
container_id=
cleanup() {
  if [[ -n "$container_id" ]]; then docker rm -f "$container_id" >/dev/null; fi
  rm -rf -- "$test_dir"
}
trap cleanup EXIT
python3 - "$test_dir" <<'PY'
import pathlib, secrets, sys
p = pathlib.Path(sys.argv[1])
token = secrets.token_hex(32)
(p / 'token').write_text(token)
(p / 'server.env').write_text('BAO_DEV_ROOT_TOKEN_ID=' + token + '\nBAO_DEV_LISTEN_ADDRESS=0.0.0.0:8200\n')
PY
container_id=$(docker run -d --rm --log-driver=none --cap-add=IPC_LOCK \
  -p 127.0.0.1::8200 --env-file "$test_dir/server.env" \
  openbao/openbao:2.7.1 server -dev)
test_port=$(docker port "$container_id" 8200/tcp)
export BAOCTL_TEST_ADDRESS="http://$test_port"
export BAOCTL_TEST_TOKEN_FILE="$test_dir/token"
python3 - <<'PY'
import os, time, urllib.request
for attempt in range(50):
    try:
        with urllib.request.urlopen(os.environ['BAOCTL_TEST_ADDRESS'] + '/v1/sys/health', timeout=1) as r:
            if r.status == 200:
                break
    except Exception:
        time.sleep(.2)
else:
    raise SystemExit('Disposable OpenBao did not become ready')
PY
go test -count=1 -run TestOpenBao271Integration -v ./internal/baoctl
