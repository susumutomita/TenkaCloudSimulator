"""Exercise distinct query-slot and returned-(timestamp,unit) 1,440 boundaries."""
from datetime import datetime, timedelta, timezone
from importlib.util import module_from_spec, spec_from_file_location
import json
from pathlib import Path
from tempfile import TemporaryDirectory

spec = spec_from_file_location('witness', Path(__file__).with_name('tenka-58-independent-stock-witness.py'))
witness = module_from_spec(spec)
spec.loader.exec_module(witness)


def main():
    end = datetime.now(timezone.utc).replace(second=0, microsecond=0) - timedelta(minutes=4)
    start = end - timedelta(minutes=720)
    metric = 'ReturnedPointBoundary'
    with TemporaryDirectory(prefix='tenka-cw-boundary-') as directory:
        db = Path(directory)/'metrics.sqlite'
        with witness.running(db=db, authenticated=True) as (endpoint, token):
            sdk = witness.client(endpoint, token)
            query = dict(MetricName=metric, StartTime=start, EndTime=end, Unit=None, Statistics=['Sum'], Period=60)
            for offset in range(0, 720, 10):
                observations = []
                for minute in range(offset, offset + 10):
                    t = start + timedelta(minutes=minute)
                    for unit, value in [('Seconds', minute), ('Count', -minute)]:
                        observations.append(witness.point(value, t, MetricName=metric, Unit=unit))
                sdk.put_metric_data(Namespace=witness.NAMESPACE, MetricData=observations)
            data = witness.get(sdk, start, **query)
            assert len(data) == 1440
            for minute in range(720):
                t = start + timedelta(minutes=minute)
                assert (data[t, 'Seconds']['Sum'], data[t, 'Count']['Sum']) == (minute, -minute)
            sdk.put_metric_data(Namespace=witness.NAMESPACE, MetricData=[witness.point(1, start, MetricName=metric, Unit='Bytes')])
            code = witness.reject(400, lambda: witness.get(sdk, start, **query))
            assert len(witness.get(sdk, start, **{**query, 'Unit':'Seconds'})) == 720
            assert len(witness.get(sdk, start, **{**query, 'Unit':'Bytes'})) == 1
            empty = {**query, 'MetricName':'Nonexistent', 'StartTime':end-timedelta(minutes=1440)}
            assert witness.get(sdk, start, **empty) == {}
            slot_code = witness.reject(400,lambda: witness.get(sdk, start, **{**empty, 'StartTime':end-timedelta(minutes=1441)}))
    assert not db.exists() and witness.AUDIT['call'] == witness.AUDIT['send'] == witness.AUDIT['response']
    print(json.dumps({'returnedBoundary':1440,'returnedExtra':[1441,code],'slotBoundary':1440,'slotExtra':[1441,slot_code],'audit':witness.AUDIT,'cleanup':True},sort_keys=True))

if __name__ == '__main__':
    main()
