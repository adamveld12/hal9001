-- hal9001 schema v4: the script of an assist draft that the user last tested or approved.
-- An agent-invoked test of a draft runs that script with real credentials, so it only runs unasked
-- while the script equals this one. approved_script is its canonical text (for diffs against a
-- changed script), approved_script_hash its identity.
ALTER TABLE assist_sessions ADD COLUMN approved_script TEXT NOT NULL DEFAULT '';
ALTER TABLE assist_sessions ADD COLUMN approved_script_hash TEXT NOT NULL DEFAULT '';
