import json
from pathlib import Path

Path("/output/report.txt").write_text(
    "Hello from zhizhi-agent-runtime\n", encoding="utf-8"
)
print(json.dumps({
    "message": "Hello from zhizhi-agent-runtime",
    "success": True,
}))
