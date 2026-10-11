"""Check MySQL grants before tables exist, without changing application data."""
import acceptance as a
import uuid
table="reader_probe_"+uuid.uuid4().hex
try:
    a.sql("GRANT SELECT ON agentmesh.`"+table+"` TO 'runner_reader'@'%'")
    a.sql("REVOKE SELECT ON agentmesh.`"+table+"` FROM 'runner_reader'@'%'")
    print("PASS MySQL supports exact table SELECT grant before table creation")
except RuntimeError:
    raise RuntimeError("fresh control bootstrap must defer exact table grants until migration") from None
