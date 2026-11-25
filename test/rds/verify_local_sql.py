"""Verify #130's fixed SQL on disposable PostgreSQL, never a guest or external DB."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import signal
import socket
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent
ROLE = "tc_rds_fixture"
DATABASE = "tc_rds_acceptance"
LOOPBACK = "127.0.0.1"
IDENTIFY = b"SELECT pg_catalog.current_setting('cluster_name');\n"
ROLLBACK = """BEGIN;
INSERT INTO tc_rds_probe.roundtrip (nonce) VALUES (current_setting('tc_rds.run_nonce'));
ROLLBACK;"""
SETUP = f"""
CREATE SCHEMA tc_rds_probe;
CREATE TABLE tc_rds_probe.roundtrip
    (nonce text PRIMARY KEY CHECK (nonce ~ '^[0-9a-f]{{64}}$'));
CREATE ROLE {ROLE} NOLOGIN;
GRANT USAGE ON SCHEMA tc_rds_probe TO {ROLE};
GRANT INSERT (nonce), SELECT (nonce) ON tc_rds_probe.roundtrip TO {ROLE};
SELECT current_setting('server_version_num') AS version_num;
"""


def require(condition, message):
    if not condition:
        # Do not print raw logs, SQL statements, passwords or nonce values.
        raise RuntimeError(message)


class ClusterMismatch(RuntimeError):
    """No fixture SQL was sent on the unverified connection."""


@contextmanager
def cluster(pg_bin, require_psql=False):
    names = ("initdb", "postgres", "psql") if require_psql else ("initdb", "postgres")
    require(all((pg_bin / name).is_file() and os.access(pg_bin / name, os.X_OK) for name in names),
            "--pg-bin lacks required PostgreSQL executables: " + ", ".join(names))
    bootstrap = "tc_rds_boot_" + secrets.token_hex(12)
    admin = secrets.token_hex(32)
    identity = "tc_rds_" + secrets.token_hex(16)
    with tempfile.TemporaryDirectory(prefix="tc-rds-sql-") as home:
        data = Path(home) / "data"
        env = {"PATH": "/usr/bin:/bin", "LC_ALL": "C", "HOME": home}
        init = subprocess.run(
            [pg_bin / "initdb", "-D", data, "-U", bootstrap, "--auth-local=reject", "--auth-host=scram-sha-256",
             "--encoding=UTF8", "--no-locale", "--pwfile=/dev/stdin"],
            input=admin + "\n", capture_output=True, text=True, timeout=45, env=env)
        require(init.returncode == 0, "disposable PostgreSQL initialization failed")
        # Only the new owned data directory declares this read-only server setting.
        with (data / "postgresql.conf").open("a", encoding="ascii") as conf:
            conf.write(f"\ncluster_name = '{identity}'\n")
        yield data, env, bootstrap, admin, identity


def set_nonce(value, script):
    require(re.fullmatch(r"[0-9a-f]{64}", value), "invalid generated test value")
    return f"SET tc_rds.run_nonce = '{value}';\n" + script


def verify(pg_bin):
    nonce, rollback_nonce = (secrets.token_hex(32) for _ in range(2))
    digest = hashlib.sha256(nonce.encode("ascii")).hexdigest()
    with cluster(pg_bin) as (data, env, *_):

        def run(script, value=None):
            # Each call starts a NEW real PostgreSQL backend against the same data directory.
            # --single does not use a socket or authenticate a client; it is not the guest path.
            if value is not None:
                script = f"SET SESSION AUTHORIZATION {ROLE};\n" + set_nonce(value, script)
            proc = subprocess.run(
                [pg_bin / "postgres", "--single", "-D", data, "-j", "-c", "log_line_prefix=%e ",
                 "-c", "log_min_error_statement=panic", "postgres"],
                input=script.strip() + "\n\n", capture_output=True, text=True, timeout=15, env=env)
            # Standalone postgres can exit zero despite a SQL error. Check server SQLSTATE too.
            require(proc.returncode == 0 and not re.search(r"^\w{5} (ERROR|FATAL|PANIC):", proc.stderr, re.M),
                    "PostgreSQL failed to execute the fixture SQL")
            return re.findall(r'^\s*1: (\w+) = "([^"\n]*)"\s+\(typeid =', proc.stdout, re.M)

        version = run(SETUP)
        require(len(version) == 1 and version[0][0] == "version_num" and version[0][1].isdigit(),
                "could not confirm PostgreSQL version")
        write, read = ((HERE / name).read_text() for name in ("write_nonce.sql", "read_nonce.sql"))
        require(run(read, nonce) == [], "fresh nonce already visible")
        require(run(write, nonce) == [("nonce_digest", digest)], "write did not return the expected row")
        require(run(read, nonce) == [("nonce_digest", digest)], "fresh backend did not read committed nonce")
        require(run(ROLLBACK, rollback_nonce) == [],
                "rollback control failed")
        require(run(read, rollback_nonce) == [], "rolled-back nonce visible after restart")
        return {"postgres_version_num": version[0][1], "committed_rows_after_restart": 1,
                "rolled_back_rows_after_restart": 0, "scope": "standalone-postgresql-engine-only"}


@contextmanager
def loopback_server(pg_bin, data, env):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as reservation:
        reservation.bind((LOOPBACK, 0))
        port = str(reservation.getsockname()[1])
    log_path = data.parent / "postgres.log"
    with log_path.open("wb") as log:
        process = subprocess.Popen(
            [pg_bin / "postgres", "-D", data, "-h", LOOPBACK, "-k", "", "-p", port,
             "-c", "password_encryption=scram-sha-256", "-c", "log_line_prefix=%e ",
             "-c", "log_min_error_statement=panic", "-c", "ssl=off"],
            stdin=subprocess.DEVNULL, stdout=log, stderr=log, env=env, start_new_session=True)
    pid_path = data / "postmaster.pid"
    started = False
    try:
        # Only use our own newly started server; an unrelated port responder is not readiness.
        deadline = time.monotonic() + 12
        while process.poll() is None and time.monotonic() < deadline:
            if b"database system is ready to accept connections" in log_path.read_bytes():
                owner = pid_path.read_text().splitlines()
                require(len(owner) >= 4 and owner[0] == str(process.pid) and owner[3] == port,
                        "postmaster identity does not match the fixture's loopback server")
                started = True
                yield port, log_path
                return
            time.sleep(.05)
        raise RuntimeError("private PostgreSQL did not start on numeric loopback")
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)  # only this disposable server's process group
                process.wait(timeout=5)
                raise RuntimeError("private PostgreSQL did not shut down before the deadline") from None
        if started:
            require(process.returncode == 0 and not pid_path.exists(),
                    "private PostgreSQL did not shut down cleanly")


def confirm_identity(client, expected):
    # The expected random name is compared here, not disclosed to an unverified peer.
    reply = bytearray()
    expected_line = (expected + "\n").encode("ascii")
    bound = len(expected_line) + 1
    client.stdin.write(IDENTIFY)
    client.stdin.flush()
    with selectors.DefaultSelector() as waiting:
        waiting.register(client.stdout, selectors.EVENT_READ)
        deadline = time.monotonic() + 4
        while len(reply) < bound and b"\n" not in reply:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not waiting.select(remaining):
                break
            part = os.read(client.stdout.fileno(), bound - len(reply))
            if not part:
                break
            reply.extend(part)
    if not secrets.compare_digest(bytes(reply), expected_line):
        raise ClusterMismatch("unrecognized PostgreSQL cluster; no fixture SQL sent")


def pinned_psql(args, env, expected, script):
    require("\\" not in script, "fixture script must not contain psql meta-commands")
    with subprocess.Popen(args, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE) as client:
        try:
            confirm_identity(client, expected)
            # Same connection. The noninteractive psql pipe does not reconnect a lost session.
            stdout, _ = client.communicate(input=script.encode("ascii"), timeout=10)
            return subprocess.CompletedProcess(args, client.returncode, stdout.decode("ascii"))
        finally:
            if client.poll() is None:
                client.kill()
                client.wait(timeout=5)


def verify_scram(pg_bin):
    write, read = ((HERE / name).read_text() for name in ("write_nonce.sql", "read_nonce.sql"))
    nonce, rollback_nonce, good = (secrets.token_hex(32) for _ in range(3))
    wrong = ("0" if good[0] != "0" else "1") + good[1:]
    digest = hashlib.sha256(nonce.encode("ascii")).hexdigest()

    with cluster(pg_bin, require_psql=True) as (data, env, bootstrap, admin, identity):
        def connect(port, script, value=None, *, username=ROLE, password=good, database=DATABASE,
                    expected_identity=identity):
            args = [pg_bin / "psql", "-XqwAt", "-h", LOOPBACK, "-p", port, "-d", database,
                    "-U", username, "-v", "ON_ERROR_STOP=1"]
            client_env = {**env, "PGPASSWORD": password, "PGCONNECT_TIMEOUT": "2", "PGSSLMODE": "disable"}
            if script is None:
                # Login-only negative probe: it never sends fixture SQL to any server.
                return subprocess.run(args, input="", text=True, capture_output=True,
                                      timeout=10, env=client_env)
            if value is not None:
                script = set_nonce(value, script)
            return pinned_psql(args, client_env, expected_identity, script)


        def output(result, message):
            require(result.returncode == 0, message)
            return result.stdout.splitlines()

        with loopback_server(pg_bin, data, env) as (port, log):
            def setup(script, database=DATABASE):
                return connect(port, script, username=bootstrap, password=admin, database=database)

            require(output(setup(f"CREATE DATABASE {DATABASE};", "postgres"), "database creation failed") == [],
                    "database creation returned unexpected data")
            version = output(setup(SETUP), "SQL fixture setup failed")
            require(len(version) == 1 and version[0].isdigit(), "engine version unreadable")
            config = f"""
                ALTER ROLE {ROLE} LOGIN PASSWORD '{good}';
                SELECT ('127.0.0.1' = current_setting('listen_addresses')
                  AND inet_server_addr() = inet '127.0.0.1'
                  AND '' = current_setting('unix_socket_directories')
                  AND EXISTS (SELECT 1 FROM pg_authid WHERE rolname = '{ROLE}'
                    AND NOT rolsuper AND rolcanlogin AND rolpassword LIKE 'SCRAM-SHA-256$%'));
                """
            require(output(setup(config), "SCRAM fixture role setup failed") == ["t"],
                    "server must be loopback-only and use a non-superuser SCRAM role")
            foreign_identity = identity[:-1] + ("0" if identity[-1] != "0" else "1")
            blocked_nonce = secrets.token_hex(32)
            try:
                connect(port, write, blocked_nonce, expected_identity=foreign_identity)
            except ClusterMismatch:
                pass
            else:
                raise RuntimeError("a mismatched cluster identity accepted fixture SQL")
            require(output(connect(port, read, blocked_nonce), "wrong-cluster check read failed") == [],
                    "a write was sent before confirming the cluster identity")

            require(output(connect(port, read, nonce), "initial read failed") == [], "fresh nonce already visible")
            require(output(connect(port, write, nonce), "write/commit failed") == [digest],
                    "write did not return the expected row")
            require(output(connect(port, read, nonce), "fresh authenticated read failed") == [digest],
                    "fresh authenticated session did not read committed nonce")

            offset = log.stat().st_size
            denied = connect(port, None, password=wrong)
            # A bad role, SQL error, TLS failure, or bounced socket cannot count as password refusal.
            with log.open("rb") as fd:
                fd.seek(offset)
                auth_failure = f'28P01 FATAL:  password authentication failed for user "{ROLE}"'.encode()
                require(denied.returncode == 2 and not denied.stdout and auth_failure in fd.read().splitlines(),
                        "wrong fixture password was not refused with SQLSTATE 28P01")
            require(output(connect(port, ROLLBACK, rollback_nonce), "rollback control failed") == [],
                    "rollback control returned unexpected data")

        with loopback_server(pg_bin, data, env) as (port, _):
            require(output(connect(port, read, nonce), "authenticated restart read failed") == [digest],
                    "committed row was not readable after server restart")
            require(output(connect(port, read, rollback_nonce), "restart rollback inspection failed") == [],
                    "rolled-back row was visible after server restart")
    return {"postgres_version_num": version[0], "wrong_password_sqlstate": "28P01",
            "committed_rows_fresh_session": 1, "committed_rows_after_restart": 1,
            "rolled_back_rows_after_restart": 0, "wrong_cluster_rows_read": 0,
            "scope": "host-ipv4-loopback-scram-only"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pg-bin", type=Path, required=True, help="trusted local PostgreSQL bin directory")
    parser.add_argument("--tcp-scram", action="store_true", help="verify actual psql login on numeric loopback")
    args = parser.parse_args()
    try:
        run = verify_scram if args.tcp_scram else verify
        print(json.dumps(run(args.pg_bin.resolve()), sort_keys=True))
    except (RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        if isinstance(error, RuntimeError):
            message = str(error)
        elif isinstance(error, OSError):
            message = f"PostgreSQL fixture system operation failed: {error.strerror}"
        else:
            message = "PostgreSQL fixture timed out"
        parser.exit(1, message + "\n")
