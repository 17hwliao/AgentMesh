"""Collect an actual selectivity benchmark; report the measured level honestly."""
import json
from pathlib import Path
import time
import uuid
import acceptance as a
import importlib.util

spec=importlib.util.spec_from_file_location("expanded",Path(__file__).with_name("acceptance-expanded.py"))
e=importlib.util.module_from_spec(spec);spec.loader.exec_module(e)
delivery="selectivity-"+uuid.uuid4().hex
query="SELECT COUNT(*) AS total FROM orders WHERE user_id=42 AND status='paid'"
candidate={"table":"orders","index_name":"idx_cand_selectivity","columns":[{"name":"user_id"},{"name":"status"}]}
auth=a.secret("sentinel_token")
e.call("/jobs",{"delivery_id":delivery,"pr_number":202,"sql":query,"candidate_spec":candidate},expected=202)
e.wait(delivery,auth)
_,headers=e.call("/jobs/"+delivery+"/report")
sha=next(v for k,v in headers.items() if k.lower()=="x-sentinel-report-sha256")
e.call("/jobs/"+delivery+"/approval",{"report_sha256":sha,"decision":"approved","reason":"acceptance selectivity controlled benchmark"},expected=201)
bench="measured-"+uuid.uuid4().hex
e.call("/jobs/"+delivery+"/benchmark",{"report_sha256":sha,"delivery_id":bench},expected=202)
e.wait(bench,auth)
report,_=e.call("/jobs/"+bench+"/report")
p=report["performance"]
assert p["result_equivalent"] and p["snapshot_sha256"] and p["workload_sha256"]
assert len(p["BaselineCalibration"]["raw_ms"])==20 and len(p["CandidateMeasurement"]["raw_ms"])==5
assert p["write_cost"]["rows_per_round"]==256
assert p["evidence_level"] in {"L1","L2"} and p["evidence_level"]!="L3"
(Path(__file__).parent.parent/"validation"/"measured-selectivity.json").write_text(json.dumps(p,indent=2)+"\n",encoding="utf-8")
print(json.dumps({"measured_level":p["evidence_level"],"read_claim_eligible":p["eligible_for_performance_claim"],"write_claim_eligible":p["write_cost"]["eligible"],"baseline_median_ms":p["BaselineMeasurement"]["median_ms"],"candidate_median_ms":p["CandidateMeasurement"]["median_ms"],"relative_delta":p["relative_delta"]}))
