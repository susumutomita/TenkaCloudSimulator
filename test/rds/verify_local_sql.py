"""Verify #130's fixed SQL on disposable PostgreSQL, never a guest or external DB."""
import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import selectors
import signal
import socket
import stat
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


class PostmasterNotReady(RuntimeError):
    """The exact owned loopback process is not available for a new connection."""


class UnconfirmedPostgresCleanup(RuntimeError):
    """Do not remove or restart a cluster whose shutdown is unconfirmed."""


class FixtureUnwind(Exception):
    """Exercise releasing a real postmaster on exceptional context exit."""


@contextmanager
def cluster(pg_bin, require_psql=False):
    names = ("initdb", "postgres", "psql") if require_psql else ("initdb", "postgres")
    require(all((pg_bin / name).is_file() and os.access(pg_bin / name, os.X_OK) for name in names),
            "--pg-bin lacks required PostgreSQL executables: " + ", ".join(names))
    bootstrap = "tc_rds_boot_" + secrets.token_hex(12)
    admin = secrets.token_hex(32)
    identity = "tc_rds_" + secrets.token_hex(16)
    home = Path(tempfile.mkdtemp(prefix="tc-rds-sql-")).resolve()
    retain = False
    try:
        data = home / "data"
        env = {"PATH": "/usr/bin:/bin", "LC_ALL": "C", "HOME": str(home)}
        init = subprocess.run(
            [pg_bin / "initdb", "-D", data, "-U", bootstrap, "--auth-local=reject", "--auth-host=scram-sha-256",
             "--encoding=UTF8", "--no-locale", "--pwfile=/dev/stdin"],
            input=admin + "\n", capture_output=True, text=True, timeout=45, env=env)
        require(init.returncode == 0, "disposable PostgreSQL initialization failed")
        # Only the new owned data directory declares this read-only server setting.
        with (data / "postgresql.conf").open("a", encoding="ascii") as conf:
            conf.write(f"\ncluster_name = '{identity}'\n")
        yield data, env, bootstrap, admin, identity
    except UnconfirmedPostgresCleanup:
        retain = True
        raise
    finally:
        if not retain:
            shutil.rmtree(home)


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


class OwnedLoopbackPostgres:
    """One foreground child, one disposable directory, one numeric loopback bind.

    This single-threaded fixture owns the Popen object and all its waits/signals.
    It never adopts a PID from a file, a process name or a network responder.
    """

    def __init__(self, process, data, port, log):
        self._process = process
        self._data = data
        self._port = port
        self.log = log
        self._pid_file = data / "postmaster.pid"
        self._ready_record = None
        self._revoked = False
        self._launch_pending = False
        self.stopped = False

    def start(self, pg_bin, env, log):
        require(self._process is None and not self._launch_pending and not self._revoked,
                "owned PostgreSQL process can only be acquired once")
        # If Popen does not return a usable handle, Python cannot infer ownership
        # from a lock file or assert that no detached process ever started.
        self._launch_pending = True
        self._process = subprocess.Popen(
            [pg_bin / "postgres", "-D", self._data, "-h", LOOPBACK, "-k", "", "-p", self._port,
             "-c", "password_encryption=scram-sha-256", "-c", "log_line_prefix=%e ",
             "-c", "log_min_error_statement=panic", "-c", "ssl=off"],
            stdin=subprocess.DEVNULL, stdout=log, stderr=log, env=env, start_new_session=True)
        self._launch_pending = False

    def _ready_witness(self):
        # Format is PostgreSQL's native data-directory lock from 10 onward.
        # Never follow a substituted link or block on a substituted file type.
        try:
            fd = os.open(self._pid_file, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError:
            return None
        with os.fdopen(fd, "rb") as lock:
            info = os.fstat(lock.fileno())
            require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and
                    not info.st_mode & 0o077 and info.st_nlink == 1,
                    "PostgreSQL lock is not a private regular file")
            value = lock.read(4097)
        require(len(value) <= 4096, "PostgreSQL lock exceeded the owned record bound")
        lines = value.split(b"\n")
        # PostgreSQL can write the lock file in pieces before publishing readiness.
        if len(lines) < 9 or lines[7].strip() != b"ready":
            return None
        require(lines[0] == str(self._process.pid).encode("ascii") and lines[1] == os.fsencode(self._data) and
                re.fullmatch(rb"[1-9][0-9]{0,15}", lines[2]) is not None and lines[3] == self._port.encode("ascii") and
                lines[4] == b"" and lines[5] == LOOPBACK.encode("ascii"),
                "PostgreSQL lock did not identify the owned loopback child")
        return tuple(lines[:6])

    def await_ready(self):
        deadline = time.monotonic() + 12
        while (not self._revoked and self._process is not None and
               self._process.poll() is None and time.monotonic() < deadline):
            witness = self._ready_witness()
            if witness is not None and self._process.poll() is None:
                self._ready_record = witness
                return
            time.sleep(.05)
        raise PostmasterNotReady("owned PostgreSQL did not become ready on numeric loopback")

    def connection_port(self):
        # This lease is only a prerequisite for psql. Each actual session still
        # verifies the random cluster identity before sending any fixture SQL.
        if (self._revoked or self._ready_record is None or self._process.poll() is not None or
                self._ready_witness() != self._ready_record or self._process.poll() is not None):
            raise PostmasterNotReady("owned PostgreSQL connection lease is no longer ready")
        return self._port

    def _lock_remains(self):
        try:
            self._pid_file.lstat()  # A dangling symlink is not proof of removal.
        except FileNotFoundError:
            return False
        return True

    def close(self):
        self._revoked = True
        if self._process is None:
            if self._launch_pending:
                raise UnconfirmedPostgresCleanup("owned PostgreSQL launch was not confirmed; private workspace retained")
            return
        try:
            self._confirm_direct_child_shutdown()
        except (OSError, KeyboardInterrupt):
            raise UnconfirmedPostgresCleanup("owned PostgreSQL shutdown interrupted; private workspace retained") from None

    def _confirm_direct_child_shutdown(self):
        if self._process.poll() is not None:
            if self._lock_remains():
                raise UnconfirmedPostgresCleanup("owned PostgreSQL exited with its lock present; private workspace retained")
            if self._ready_record is not None:
                raise PostmasterNotReady("owned PostgreSQL exited before fast shutdown")
            return
        try:
            # Fast shutdown waits for the real postmaster to finish its workers.
            # Signal only our unreaped direct child, never someone else's PID/group.
            self._process.send_signal(signal.SIGINT)
            status = self._process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            # Bound the emergency stop without pretending it was a clean close.
            self._process.send_signal(signal.SIGQUIT)
            try:
                self._process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._process.kill()
                try:
                    self._process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
            raise UnconfirmedPostgresCleanup("owned PostgreSQL fast shutdown timed out; private workspace retained") from None
        if status != 0 or self._lock_remains():
            raise UnconfirmedPostgresCleanup("owned PostgreSQL fast shutdown was not confirmed; private workspace retained")
        self.stopped = True


@contextmanager
def defer_startup_sigint():
    """Defer normal Python Ctrl-C only through local child-handle acquisition."""
    previous = signal.getsignal(signal.SIGINT)
    if previous == signal.SIG_IGN:
        yield
        return
    require(previous == signal.default_int_handler,
            "PostgreSQL fixture requires ordinary Python SIGINT handling")
    interrupted = False

    def note_sigint(_number, _frame):
        nonlocal interrupted
        interrupted = True

    # A caught signal disposition resets on exec. Unlike blocking SIGINT in
    # the parent, this leaves PostgreSQL able to receive its fast-stop signal.
    try:
        signal.signal(signal.SIGINT, note_sigint)
        yield
    finally:
        signal.signal(signal.SIGINT, previous)
    if interrupted:
        raise KeyboardInterrupt


@contextmanager
def loopback_server(pg_bin, data, env):
    # Without single-threaded reaping and the normal child disposition, a
    # numeric PID alone must not authorize any process signal.
    require(signal.getsignal(signal.SIGCHLD) == signal.SIG_DFL,
            "PostgreSQL fixture requires ordinary direct-child ownership")
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as reservation:
        reservation.bind((LOOPBACK, 0))
        port = str(reservation.getsockname()[1])
    log_path = data.parent / "postgres.log"
    server = OwnedLoopbackPostgres(None, data, port, log_path)
    try:
        # Cleanup is armed before either the process or log is acquired. In
        # particular, log.__exit__ and a deferred Ctrl-C cannot orphan a handle.
        with defer_startup_sigint():
            with log_path.open("wb") as log:
                server.start(pg_bin, env, log)
        server.await_ready()
        yield server
    finally:
        server.close()


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
        def connect(server, script, value=None, *, username=ROLE, password=good, database=DATABASE,
                    expected_identity=identity):
            args = [pg_bin / "psql", "-XqwAt", "-h", LOOPBACK, "-p", server.connection_port(), "-d", database,
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

        with loopback_server(pg_bin, data, env) as first:
            def setup(script, database=DATABASE):
                return connect(first, script, username=bootstrap, password=admin, database=database)

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
                connect(first, write, blocked_nonce, expected_identity=foreign_identity)
            except ClusterMismatch:
                pass
            else:
                raise RuntimeError("a mismatched cluster identity accepted fixture SQL")
            require(output(connect(first, read, blocked_nonce), "wrong-cluster check read failed") == [],
                    "a write was sent before confirming the cluster identity")

            require(output(connect(first, read, nonce), "initial read failed") == [], "fresh nonce already visible")
            require(output(connect(first, write, nonce), "write/commit failed") == [digest],
                    "write did not return the expected row")
            require(output(connect(first, read, nonce), "fresh authenticated read failed") == [digest],
                    "fresh authenticated session did not read committed nonce")

            offset = first.log.stat().st_size
            denied = connect(first, None, password=wrong)
            # A bad role, SQL error, TLS failure, or bounced socket cannot count as password refusal.
            with first.log.open("rb") as fd:
                fd.seek(offset)
                auth_failure = f'28P01 FATAL:  password authentication failed for user "{ROLE}"'.encode()
                require(denied.returncode == 2 and not denied.stdout and auth_failure in fd.read().splitlines(),
                        "wrong fixture password was not refused with SQLSTATE 28P01")
            require(output(connect(first, ROLLBACK, rollback_nonce), "rollback control failed") == [],
                    "rollback control returned unexpected data")

        require(first.stopped, "first owned PostgreSQL close was not confirmed")
        try:
            with loopback_server(pg_bin, data, env) as reopened:
                try:
                    first.connection_port()
                except PostmasterNotReady:
                    pass
                else:
                    raise RuntimeError("revoked loopback lease issued a connection after restart")
                require(output(connect(reopened, read, nonce), "authenticated restart read failed") == [digest],
                        "committed row was not readable after server restart")
                require(output(connect(reopened, read, rollback_nonce), "restart rollback inspection failed") == [],
                        "rolled-back row was visible after server restart")
                raise FixtureUnwind
        except FixtureUnwind:
            require(reopened.stopped, "exceptional owned PostgreSQL close was not confirmed")
    return {"postgres_version_num": version[0], "wrong_password_sqlstate": "28P01",
            "committed_rows_fresh_session": 1, "committed_rows_after_restart": 1,
            "rolled_back_rows_after_restart": 0, "wrong_cluster_rows_read": 0,
            "owned_confirmed_shutdowns": 2, "stale_lease_refused": True, "exceptional_exit_cleaned": True,
            "scope": "host-ipv4-loopback-scram-only"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pg-bin", type=Path, required=True, help="trusted local PostgreSQL bin directory")
    parser.add_argument("--tcp-scram", action="store_true", help="verify actual psql login on numeric loopback")
    args = parser.parse_args()
    try:
        run = verify_scram if args.tcp_scram else verify
        print(json.dumps(run(args.pg_bin.resolve()), sort_keys=True))
    except KeyboardInterrupt:
        parser.exit(130, "PostgreSQL fixture interrupted; no pass evidence\n")
    except (RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        if isinstance(error, RuntimeError):
            message = str(error)
        elif isinstance(error, OSError):
            message = f"PostgreSQL fixture system operation failed: {error.strerror}"
        else:
            message = "PostgreSQL fixture timed out"
        parser.exit(1, message + "\n")
