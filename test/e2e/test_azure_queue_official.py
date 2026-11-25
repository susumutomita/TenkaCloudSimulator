import copy
import unittest
from types import SimpleNamespace
from azure_queue_official import AcceptanceFailure, run_lifecycle

class DetectorClient:
    """Detector fixture only; not Azure protocol or SDK implementation evidence."""
    def __init__(self, fault=None):
        self.fault=fault; self.message=None; self.visible=True; self.receipt=0
    def create_queue(self): pass
    def send_message(self, content):
        self.message=SimpleNamespace(id='id', content=content, dequeue_count=0, pop_receipt='initial')
        self.visible=True
        return copy.copy(self.message)
    def peek_messages(self, **kwargs):
        return [copy.copy(self.message)] if self.message and self.visible else []
    def receive_message(self, **kwargs):
        if not self.message or not self.visible: return None
        self.visible=False; self.message.dequeue_count+=1; self.message.pop_receipt='received'
        return copy.copy(self.message)
    def update_message(self, message, **kwargs):
        self.visible=True; self.message.pop_receipt='updated'
        return copy.copy(self.message)
    def delete_message(self, message, **kwargs):
        if self.fault!='delete-noop': self.message=None
    def clear_messages(self):
        if self.fault!='clear-noop': self.message=None
    def delete_queue(self):
        if self.fault=='cleanup-error': raise RuntimeError('cleanup failed')

class DetectorTests(unittest.TestCase):
    def test_complete_fixture_is_accepted(self):
        self.assertEqual(run_lifecycle(DetectorClient())['status'],'passed')
    def test_delete_noop_rejected_before_clear(self):
        with self.assertRaisesRegex(AcceptanceFailure,'DeleteMessage no-op'):
            run_lifecycle(DetectorClient('delete-noop'))
    def test_clear_noop_rejected(self):
        with self.assertRaisesRegex(AcceptanceFailure,'ClearMessages no-op'):
            run_lifecycle(DetectorClient('clear-noop'))
    def test_cleanup_failure_not_swallowed(self):
        with self.assertRaisesRegex(RuntimeError,'cleanup failed'):
            run_lifecycle(DetectorClient('cleanup-error'))
    def test_zero_work_not_success(self):
        class NoWork(DetectorClient):
            def send_message(self, content): return None
        with self.assertRaises((AttributeError,TypeError,AcceptanceFailure)):
            run_lifecycle(NoWork())

class CaptureDetectorTests(unittest.TestCase):
    def captures(self):
        from azure_queue_official import PREFIX
        p=PREFIX+'/sdk-queue'
        return [('PUT',p,'',None),
                ('POST',p+'/messages','visibilitytimeout=0&messagettl=60','<QueueMessage><MessageText>synthetic &amp; checked</MessageText></QueueMessage>'),
                ('GET',p+'/messages','peekonly=true&numofmessages=1',None),
                ('GET',p+'/messages','visibilitytimeout=30&numofmessages=1',None),
                ('PUT',p+'/messages/synthetic-id','popreceipt=receipt%2B%2F%3D&visibilitytimeout=0','<QueueMessage><MessageText>changed &amp; checked</MessageText></QueueMessage>'),
                ('DELETE',p+'/messages/synthetic-id','popreceipt=receipt%2B%2F%3D',None),
                ('DELETE',p+'/messages','',None),('DELETE',p,'',None)]
    def test_capture_fixture_is_accepted(self):
        from azure_queue_official import validate_capture
        validate_capture(self.captures())
    def test_operation_query_mutations_are_rejected(self):
        from azure_queue_official import validate_capture
        for index,query in [(1,'visibilitytimeout=1&messagettl=60'),(2,'numofmessages=1'),
                            (3,'visibilitytimeout=0&numofmessages=1'),(4,'popreceipt=wrong&visibilitytimeout=0'),
                            (5,''),(6,'popreceipt=unrelated')]:
            captures=self.captures();method,path,_,body=captures[index];captures[index]=(method,path,query,body)
            with self.subTest(index=index),self.assertRaises(AcceptanceFailure):validate_capture(captures)
    def test_xml_missing_wrong_and_duplicate_payload_are_rejected(self):
        from azure_queue_official import validate_capture
        for body in [None,'<QueueMessage/>','<QueueMessage><MessageText>wrong</MessageText></QueueMessage>',
                     '<QueueMessage><MessageText>synthetic &amp; checked</MessageText><MessageText>extra</MessageText></QueueMessage>']:
            captures=self.captures();method,path,query,_=captures[1];captures[1]=(method,path,query,body)
            with self.subTest(body=body),self.assertRaises(AcceptanceFailure):validate_capture(captures)

if __name__=='__main__': unittest.main()
