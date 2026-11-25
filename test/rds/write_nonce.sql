-- The trusted caller sets tc_rds.run_nonce to a fresh 64-character hex value.
BEGIN;
SET LOCAL search_path = pg_catalog;
SET LOCAL statement_timeout = '5s';
INSERT INTO tc_rds_probe.roundtrip (nonce) VALUES (current_setting('tc_rds.run_nonce'))
RETURNING encode(sha256(convert_to(nonce, 'UTF8')), 'hex') AS nonce_digest;
COMMIT;
