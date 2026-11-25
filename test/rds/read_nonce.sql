-- Run on a new connection after the writer has committed.
BEGIN READ ONLY;
SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '5s';
SELECT encode(sha256(convert_to(nonce, 'UTF8')), 'hex') AS nonce_digest
FROM tc_rds_probe.roundtrip WHERE nonce = current_setting('tc_rds.run_nonce');
COMMIT;
