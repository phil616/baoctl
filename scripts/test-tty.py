#!/usr/bin/env python3
"""Drive only the disposable test container; never print terminal transcripts."""
import errno
import os
import pty
import select
import subprocess
import sys
import time

container, address = sys.argv[1:]
password = b"fictional-integration-password"


def interactive(arguments, prompts):
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp("docker", ["docker", "exec", "-it", container, "/test/baoctl", *arguments])
    transcript = b""
    pending = b""
    deadline = time.monotonic() + 25
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([fd], [], [], .1)
            if ready:
                try:
                    data = os.read(fd, 65536)
                except OSError as error:
                    if error.errno == errno.EIO:
                        break
                    raise
                if not data:
                    break
                transcript += data
                pending += data
                if prompts and prompts[0][0] in pending:
                    _, response = prompts.pop(0)
                    pending = b""
                    os.write(fd, response + b"\n")
            ended, status = os.waitpid(pid, os.WNOHANG)
            if ended:
                if os.waitstatus_to_exitcode(status) != 0:
                    raise AssertionError("interactive command failed")
                pid = None
                break
        else:
            raise AssertionError("interactive command timed out")
        if pid:
            _, status = os.waitpid(pid, 0)
            pid = None
            assert os.waitstatus_to_exitcode(status) == 0, "interactive command failed"
        assert not prompts, "not all prompts were reached"
        assert password not in transcript, "password echoed"
    finally:
        if pid:
            os.kill(pid, 9)
            os.waitpid(pid, 0)
        os.close(fd)


def login_prompts():
    return [("管理员用户名".encode(), b"test-admin"), ("管理员密码（不回显）: ".encode(), password)]


def run(*args, success=True):
    result = subprocess.run(["docker", "exec", container, "/test/baoctl", *args], capture_output=True, timeout=25)
    assert (result.returncode == 0) == success, "unexpected CLI exit status"
    assert password not in result.stdout + result.stderr, "secret in output"
    assert b"fictional-test-value" not in result.stdout + result.stderr, "env leaked"
    return result.stdout


interactive(["init", "--address", address, "--machine", "cli-machine"],
            [("本机应用".encode(), b""), ("输入 yes".encode(), b"yes"), *login_prompts()])
before = run("status")
assert "尚未登记应用".encode() in before, "empty machine status missing"
assert "接入完成：true".encode() in before, "empty machine not ready"
interactive(["init"], login_prompts())
assert run("status") == before, "repeat init changed local identity"
run("doctor")
run("sync", "app-a", success=False)
interactive(["app", "add", "app-a"], login_prompts())
run("sync", "app-a", "--expect-version", "1")
interactive(["app", "add", "app-b"], login_prompts())
run("sync", "app-b")
run("doctor")
# Register the same name independently for each environment and type.
for classified, typename, output in [
    ("test", "apps", "/run/baoctl/app-a.env.test"),
    ("dev", "apps", "/run/baoctl/app-a.env.development"),
    ("prod", "files", "/run/baoctl/app-a/prod/hello.txt"),
    ("test", "files", "/run/baoctl/app-a/test/hello.txt"),
    ("dev", "files", "/run/baoctl/app-a/dev/hello.txt"),
]:
    arguments = ["app-a", "--classified", classified, "--typename", typename]
    run("sync", *arguments, success=False)
    interactive(["app", "add", *arguments], login_prompts())
    interactive(["app", "add", *arguments], login_prompts())
    run("sync", *arguments)
    assert output.encode() in run("status"), "classified output absent from status"
run("doctor")
run("sync", "app-a", "--classified", "invalid", success=False)
run("sync", "app-a", "--typename", "invalid", success=False)
run("sync", "app-other", success=False)
run("init", success=False)
run("sync", "app-a", "--expect-version", "99", success=False)
# Explicit empty --apps skips the optional prompt; populated --apps remains supported.
for name, apps, classified, typename in [("empty", "", "prod", "apps"), ("populated", "app-a,app-b", "prod", "apps"), ("test-files", "app-a", "test", "files"), ("dev-apps", "app-a", "dev", "apps")]:
    state = "/etc/baoctl-" + name
    interactive(["--state-dir", state, "init", "--address", address,
                 "--machine", "cli-" + name, "--apps", apps,
                 "--output-dir", "/run/baoctl-" + name, "--classified", classified, "--typename", typename],
                [("输入 yes".encode(), b"yes"), *login_prompts()])
    run("--state-dir", state, "doctor")
    run("--state-dir", state, "sync", "app-a", "--classified", classified, "--typename", typename, success=bool(apps))
print("CLI terminal checks passed")
