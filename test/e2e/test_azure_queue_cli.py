"""Offline boundary tests. Doubles are never SDK acceptance."""
from contextlib import contextmanager
import tempfile
import unittest
from unittest.mock import Mock, patch
import azure_queue_cli as r

class Boundaries(unittest.TestCase):
    def test_destination(self):
        r.check_url('http://127.0.0.1:1234/a?b=1', 1234)
        for u in ['https://127.0.0.1:1234/a', 'http://localhost:1234/a',
                  'http://127.0.0.1:1235/a', 'http://x@127.0.0.1:1234/a',
                  'http://127.0.0.1:1234/a#x', 'http://example.com/a']:
            with self.subTest(u=u), self.assertRaises(r.Blocked): r.check_url(u,1234)
    def test_proxy_environment(self):
        r.check_environment({})
        for key in ['HTTP_PROXY','https_proxy','ALL_PROXY','NO_PROXY']:
            with self.subTest(key=key), self.assertRaises(r.Blocked): r.check_environment({key:'*'})
    def test_transport_rechecks(self):
        send=Mock()
        with self.assertRaises(r.Blocked): r.guarded_send(send,Mock(url='http://example.com'),1234)
        send.assert_not_called()
    def test_redirect_no_second_send(self):
        send=Mock(return_value=Mock(status_code=302))
        with self.assertRaises(r.Blocked): r.guarded_send(send,Mock(url='http://127.0.0.1:1234/a'),1234)
        self.assertEqual(send.call_count,1)
        self.assertFalse(send.call_args.kwargs['allow_redirects'])
    def test_proxy_transport(self):
        send=Mock()
        with self.assertRaises(r.Blocked): r.guarded_send(send,Mock(url='http://127.0.0.1:1234/a'),1234,proxies={'http':'x'})
        send.assert_not_called()
    def test_exception_not_success(self):
        send=Mock(side_effect=PermissionError('denied'))
        with self.assertRaises(PermissionError): r.guarded_send(send,Mock(url='http://127.0.0.1:1234/a'),1234)
        self.assertEqual(send.call_count,1)
    def test_success_transport(self):
        send=Mock(return_value=Mock(status_code=200))
        self.assertEqual(r.guarded_send(send,Mock(url='http://127.0.0.1:1234/a'),1234).status_code,200)
    def test_capability(self):
        caps={'cloudStorage':{'azure-queue':{'endpoint':'/_simulator/providers/azure-queue/tenkacloudlocal','apiVersion':'2023-11-03','operations':list(r.OPERATIONS)}}}
        r.check_capability(caps)
        caps['cloudStorage']['azure-queue']['operations'].pop()
        with self.assertRaises(r.Blocked): r.check_capability(caps)
    def test_exit_failure(self):
        p=Mock(); p.poll.return_value=1; p.wait.return_value=1
        with self.assertRaises(r.Blocked): r.stop_process(p)
    def test_exit_success(self):
        p=Mock(); p.poll.return_value=None; p.wait.return_value=0
        r.stop_process(p); p.terminate.assert_called_once();p.wait.assert_called_once()
    def test_sdk_missing_before_process(self):
        with patch.dict(r.os.environ,{},clear=True), patch.object(r.importlib.metadata,'version',side_effect=r.importlib.metadata.PackageNotFoundError('azure-storage-queue')), patch.object(r.subprocess,'Popen') as start:
            with self.assertRaises(r.importlib.metadata.PackageNotFoundError): r.run('/not-used')
            start.assert_not_called()
    def test_sdk_wrong_pin_before_process(self):
        with patch.dict(r.os.environ,{},clear=True), patch.object(r.importlib.metadata,'version',return_value='0'), patch.object(r.subprocess,'Popen') as start:
            with self.assertRaises(r.Blocked): r.run('/not-used')
            start.assert_not_called()
    def test_cleanup_failure(self):
        cleanup=Mock(side_effect=OSError('cleanup failed'))
        with self.assertRaises(OSError): r.finish(Mock(poll=Mock(return_value=0),wait=Mock(return_value=0)),cleanup)


class FakeResponseError(RuntimeError):
    """An offline response detector, never an Azure SDK or server receipt."""
    def __init__(self, status):
        self.status_code = status


class TwoInstanceBoundaryTests(unittest.TestCase):
    def test_digest_is_required_before_launch(self):
        with patch.dict(r.os.environ, {}, clear=True), patch.object(r.importlib.metadata, 'version', return_value=r.PIN), \
                patch.object(r.subprocess, 'Popen') as start:
            with self.assertRaisesRegex(r.Blocked, 'SHA-256'):
                r.run('/not-used')
            start.assert_not_called()

    def test_digest_mismatch_or_replaced_binary_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = r.Path(directory) / 'fake-binary'
            path.write_bytes(b'offline fixture')
            path.chmod(0o700)
            digest = r.hashlib.sha256(b'offline fixture').hexdigest()
            with self.assertRaisesRegex(r.Blocked, 'digest'):
                r.verify_binary(path, '0' * 64)
            checked = r.verify_binary(path, digest)
            self.assertEqual(checked.digest, digest)
            path.write_bytes(b'changed fixture')
            with self.assertRaisesRegex(r.Blocked, 'changed'):
                checked.recheck()

    def test_capability_duplicates_are_not_exact(self):
        c = {'endpoint': r.PREFIX, 'apiVersion': r.VERSION,
             'operations': [*r.OPERATIONS, 'PeekMessages']}
        with self.assertRaisesRegex(r.Blocked, 'capability'):
            r.check_capability({'cloudStorage': {'azure-queue': c}})

    def test_different_ports_and_different_tokens_are_both_required(self):
        a = r.LocalInstance(10001, 'a')
        r.distinct_instances(a, r.LocalInstance(10002, 'b'))
        for bad in (r.LocalInstance(10001, 'b'), r.LocalInstance(10002, 'a')):
            with self.assertRaisesRegex(r.Blocked, 'separate'):
                r.distinct_instances(a, bad)

    def test_denial_refuses_unexpected_failure_or_false_success(self):
        r.expect_sdk_status(FakeResponseError, 401, Mock(side_effect=FakeResponseError(401)))
        for outcome in (FakeResponseError(403), FakeResponseError(404)):
            with self.assertRaisesRegex(r.Blocked, 'HTTP 401'):
                r.expect_sdk_status(FakeResponseError, 401, Mock(side_effect=outcome))
        with self.assertRaisesRegex(r.Blocked, 'operation succeeded'):
            r.expect_sdk_status(FakeResponseError, 401, Mock(return_value=[]))

    def scripted(self, defect=None):
        # A call-order oracle using doubles only. No network, files, or SDK.
        peer = r.LocalInstance(10001, 'peer-dummy-token')
        owner = r.LocalInstance(10002, 'owner-dummy-token')
        reopened = r.LocalInstance(10003, owner.token if defect != 'new-token' else 'wrong-restart-token')
        peers = [{'id': 'peer-id', 'content': r.PEER_TEXT, 'dequeue_count': 0}]
        owned = [{'id': 'owner-id', 'content': r.OWNER_TEXT, 'dequeue_count': 0}]
        updated = [{'id': 'owner-id', 'content': r.UPDATED_TEXT, 'dequeue_count': 1}]
        b, a, again, wrong_owner, wrong_peer = (Mock() for _ in range(5))
        b.send_message.return_value = peers[0]
        a.send_message.return_value = owned[0]
        a.receive_message.return_value = dict(owned[0], dequeue_count=1, pop_receipt='old-receipt')
        b.peek_messages.side_effect = [
            FakeResponseError(404), peers, peers, peers,
            [] if defect == 'peer-changed' else peers,
            [] if defect == 'delete-no-op' else FakeResponseError(404),
        ]
        a.peek_messages.side_effect = [FakeResponseError(404), owned, owned, []]
        again.peek_messages.side_effect = [
            [] if defect != 'lease-lost' else owned, updated, [], FakeResponseError(404), FakeResponseError(404),
        ]
        again.update_message.return_value = {'pop_receipt': 'new-receipt' if defect != 'receipt-unchanged' else 'old-receipt'}
        wrong_owner.peek_messages.side_effect = None if defect == 'wrong-token-allowed' else FakeResponseError(401)
        wrong_peer.clear_messages.side_effect = FakeResponseError(401)
        planned = iter([('peer', peer), ('owner', owner), ('owner', reopened)])
        connections = {
            (peer.port, None): b, (owner.port, None): a, (reopened.port, None): again,
            (owner.port, peer.token): wrong_owner, (peer.port, owner.token): wrong_peer,
        }
        active, trace = {}, []

        @contextmanager
        def launch(name):
            expected, instance = next(planned)
            self.assertEqual(name, expected)
            self.assertNotIn(name, active)
            active[name] = instance
            trace.append(('start', instance.port))
            try:
                yield instance
            finally:
                self.assertEqual(active.pop(name), instance)
                trace.append(('stop', instance.port))

        @contextmanager
        def connect(instance, token=None):
            self.assertIn(instance, active.values())
            yield connections[(instance.port, token)]

        return launch, connect, active, trace, (a, b, again, wrong_owner, wrong_peer)

    def test_peer_remains_active_during_same_owner_restart(self):
        launch, connect, active, trace, (a, b, again, wrong_owner, wrong_peer) = self.scripted()
        with patch.object(r, 'run_lifecycle', return_value={'status': 'offline-only'}) as lifecycle:
            self.assertEqual(r.run_two_instances(launch, connect, FakeResponseError), {'status': 'offline-only'})
        self.assertEqual(trace, [
            ('start', 10001), ('start', 10002), ('stop', 10002),
            ('start', 10003), ('stop', 10003), ('stop', 10001),
        ])
        self.assertFalse(active)
        wrong_owner.peek_messages.assert_called_once_with(max_messages=1)
        wrong_peer.clear_messages.assert_called_once_with()
        a.receive_message.assert_called_once_with(visibility_timeout=300)
        again.update_message.assert_called_once_with('owner-id', pop_receipt='old-receipt',
                                                    visibility_timeout=0, content=r.UPDATED_TEXT)
        again.delete_message.assert_called_once_with('owner-id', pop_receipt='new-receipt')
        b.delete_queue.assert_called_once_with()
        lifecycle.assert_called_once_with(again)

    def test_no_receipt_on_authority_state_or_cleanup_failure(self):
        for defect in ('wrong-token-allowed', 'new-token', 'lease-lost',
                       'receipt-unchanged', 'peer-changed', 'delete-no-op'):
            with self.subTest(defect=defect):
                launch, connect, active, trace, _ = self.scripted(defect)
                with patch.object(r, 'run_lifecycle', return_value={'status': 'offline-only'}):
                    with self.assertRaises(r.Blocked):
                        r.run_two_instances(launch, connect, FakeResponseError)
                self.assertFalse(active)
                self.assertEqual(trace[-1], ('stop', 10001))


if __name__=='__main__': unittest.main()
