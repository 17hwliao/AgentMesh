import json
from pathlib import Path
import time
import acceptance as a

try:
    a.command("stop","otel-collector")
    start=time.monotonic()
    a.expect(a.request("/v1/chat/completions",a.CHAT,a.secret("bootstrap_key"))[0],200)
    elapsed=time.monotonic()-start
    assert elapsed<10,"tracing exporter outage blocked request"
    a.expect(a.request("/readyz")[0],200)
    a.expect(a.request("/readyz",port=29081)[0],200)
finally:
    a.command("start","otel-collector")
(Path(__file__).parent.parent/"validation"/"monitoring-fault.txt").write_text("PASS: collector stopped; charged fixture SSE returned 200 and both backends stayed ready; collector restored.\nRequest seconds: "+str(round(elapsed,3))+"\n",encoding="utf-8")
print("PASS collector outage remains nonblocking")
