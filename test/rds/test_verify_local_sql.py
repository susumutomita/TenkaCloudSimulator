"""Focused defensive checks for the disposable direct-child fixture."""
from contextlib import contextmanager
import os
from pathlib import Path
import signal
import subprocess
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import MagicMock, Mock, call, patch

from verify_local_sql import (
    OwnedLoopbackPostgres,
    PostmasterNotReady,
    UnconfirmedPostgresCleanup,
    cluster,
    loopback_server,
)


class OwnedLoopbackPostgresTests(unittest.TestCase):
    def setUp(self):
        self.home = TemporaryDirectory()
        self.addCleanup(self.home.cleanup)
        self.data = Path(self.home.name)
        self.lock = self.data / "postmaster.pid"
        self.port = "49491"
        self.process = Mock(pid=31459)
        self.process.poll.return_value = None
        self.owner = OwnedLoopbackPostgres(self.process, self.data, self.port, None)

    def write_lock(self, *, started="1234567", pid="31459", data=None, address="127.0.0.1", socket_dir="", state="ready   "):
        fields = [pid, str(self.data if data is None else data), started, self.port, socket_dir, address, "0 0", state]
        self.lock.write_bytes(b"\n".join(os.fsencode(field) for field in fields) + b"\n")
        self.lock.chmod(0o600)

    def test_matching_lock_pins_startup_and_stale_state_never_reissues_lease(self):
        self.write_lock()
        self.owner.await_ready()
        self.assertEqual(self.owner.connection_port(), self.port)
        self.write_lock(started="1234568")  # Same printed PID, different launch witness.
        with self.assertRaises(PostmasterNotReady):
            self.owner.connection_port()
        self.process.send_signal.assert_not_called()

    def test_pid_data_socket_and_bind_must_all_match_owned_child(self):
        for invalid in ({"pid": "1"}, {"data": "/different-private-work"},
                        {"socket_dir": "/different-private-work"}, {"address": "*"}):
            with self.subTest(invalid):
                self.write_lock(**invalid)
                with self.assertRaisesRegex(RuntimeError, "lock did not identify"):
                    self.owner.await_ready()
                with self.assertRaises(PostmasterNotReady):
                    self.owner.connection_port()
        self.process.send_signal.assert_not_called()

    def test_symbolic_lock_is_neither_read_nor_considered_removed(self):
        target = self.data / "unrelated-file"
        target.write_text("standalone fixture marker")
        self.lock.symlink_to(target)
        with self.assertRaises(OSError):
            self.owner.await_ready()
        self.process.poll.return_value = 0
        with self.assertRaises(UnconfirmedPostgresCleanup):
            self.owner.close()
        self.assertEqual(target.read_text(), "standalone fixture marker")
        self.process.send_signal.assert_not_called()

    def test_mid_startup_exit_does_not_signal_retired_pid(self):
        self.process.poll.return_value = 1
        with self.assertRaises(PostmasterNotReady):
            self.owner.await_ready()
        self.owner.close()
        self.assertFalse(self.owner.stopped)
        self.process.send_signal.assert_not_called()

    def test_close_revokes_before_signaling_and_waits_for_lock_removal(self):
        self.write_lock()
        self.owner.await_ready()

        def stop(sig):
            self.assertEqual(sig, signal.SIGINT)
            with self.assertRaises(PostmasterNotReady):
                self.owner.connection_port()
            self.lock.unlink()

        self.process.send_signal.side_effect = stop
        self.process.wait.return_value = 0
        self.owner.close()
        self.assertTrue(self.owner.stopped)
        self.process.wait.assert_called_once()
        self.process.kill.assert_not_called()

    def test_failed_cleanup_uses_only_the_owned_child_and_fails_closed(self):
        timeout = subprocess.TimeoutExpired("owned-direct-child", 12)
        self.process.wait.side_effect = [timeout, timeout, 0]
        with self.assertRaises(UnconfirmedPostgresCleanup):
            self.owner.close()
        self.assertFalse(self.owner.stopped)
        self.assertEqual(self.process.send_signal.call_args_list, [call(signal.SIGINT), call(signal.SIGQUIT)])
        self.process.kill.assert_called_once()
        with self.assertRaises(PostmasterNotReady):
            self.owner.connection_port()

    @contextmanager
    def stub_cluster(self):
        def initialize(args, **kwargs):
            data = args[args.index("-D") + 1]
            data.mkdir()
            (data / "postgresql.conf").touch()
            return subprocess.CompletedProcess(args, 0)

        for name in ("initdb", "postgres"):
            exe = self.data / name
            exe.touch()
            exe.chmod(0o700)
        home = self.data / "owned-private"
        home.mkdir(mode=0o700)
        with patch("verify_local_sql.tempfile.mkdtemp", return_value=str(home)), \
                patch("verify_local_sql.subprocess.run", side_effect=initialize):
            with cluster(self.data) as (data, *_):
                yield data

    @contextmanager
    def stub_launch_io(self, log_close_error=None):
        reservation, log = MagicMock(), MagicMock()
        reservation.__enter__.return_value.getsockname.return_value = ("127.0.0.1", int(self.port))
        log.__exit__.side_effect = log_close_error
        with patch("verify_local_sql.socket.socket", return_value=reservation), \
                patch("verify_local_sql.Path.open", return_value=log):
            yield

    def test_uncertain_signal_leaves_cluster_workspace_instead_of_removing_it(self):
        with self.assertRaises(UnconfirmedPostgresCleanup):
            with self.stub_cluster() as data:
                self.process.send_signal.side_effect = OSError("test-only wait failure")
                OwnedLoopbackPostgres(self.process, data, self.port, None).close()
        self.assertTrue((self.data / "owned-private").is_dir())

    def test_log_close_failure_reaps_acquired_child(self):
        self.process.wait.return_value = 0
        with self.stub_launch_io(OSError("test-only log close")), \
                patch("verify_local_sql.subprocess.Popen", return_value=self.process):
            with self.assertRaisesRegex(OSError, "test-only log close"):
                with loopback_server(self.data, self.data, {}):
                    self.fail("no ready lease must escape a log-close failure")
        self.process.send_signal.assert_called_once_with(signal.SIGINT)
        self.process.wait.assert_called_once_with(timeout=12)

    def test_ctrl_c_is_deferred_until_acquired_child_can_be_cleaned(self):
        previous = signal.signal(signal.SIGINT, signal.default_int_handler)
        self.addCleanup(signal.signal, signal.SIGINT, previous)
        finished_spawn = []
        self.process.wait.return_value = 0

        def interrupted_spawn(*args, **kwargs):
            signal.raise_signal(signal.SIGINT)
            finished_spawn.append(True)
            return self.process

        with self.stub_launch_io(), patch("verify_local_sql.subprocess.Popen", side_effect=interrupted_spawn):
            with self.assertRaises(KeyboardInterrupt):
                with loopback_server(self.data, self.data, {}):
                    self.fail("no ready lease must escape Ctrl-C during acquisition")
        self.assertEqual(finished_spawn, [True])
        self.assertIs(signal.getsignal(signal.SIGINT), signal.default_int_handler)
        self.process.send_signal.assert_called_once_with(signal.SIGINT)
        self.process.wait.assert_called_once_with(timeout=12)

    def test_interrupted_spawn_without_a_returned_handle_retains_the_workspace(self):
        with self.assertRaisesRegex(UnconfirmedPostgresCleanup, "launch was not confirmed"):
            with self.stub_cluster() as data:
                with self.stub_launch_io(), patch("verify_local_sql.subprocess.Popen", side_effect=KeyboardInterrupt):
                    with loopback_server(self.data, data, {}):
                        self.fail("no ready lease may escape an uncertain launch")
        self.assertTrue((self.data / "owned-private" / "data").is_dir())
        self.process.send_signal.assert_not_called()


if __name__ == "__main__":
    unittest.main()
