# Python SDK

Python 3.10+，运行时仅使用标准库。`backend_client.py` 可随应用源码导入。私有 Key 文件不进 Git；生产使用 HTTPS 与可信 CA。

```python
from backend_client import Client

client = Client("https://api.example.com", "/private/tenant-key")
with client.chat([{"role": "user", "content": "hello"}]) as chunks:
    for chunk in chunks:
        for choice in chunk.get("choices", []):
            print(choice.get("delta", {}).get("content", ""), end="")
print(client.balance())
```

`BackendError` 提供 HTTP status 和经限制的错误 code；`ProtocolError` 表示流被截断、非法事件或超过大小限制。事件/行上限 128 KiB、累计响应上限 4 MiB；timeout 是单次网络操作的界限，流消费方还应按业务需要控制总时长。拒绝 HTTP 重定向，避免转发 Bearer。

提前停止消费时退出 `with`，让上游收到关闭。收到部分内容不等于完成；SDK 只接受完整事件终止符后的 `[DONE]`。未知上游 usage 仍可能冻结金额等待对账。收费请求不自动重试；SQL 使用固定 delivery_id，并保持 SQL、候选和 PR 信息相同，服务器会拒绝冲突重试。

`example.py` 演示流、账单、SQL report 与幂等 retry；联合一键演示会实际执行该例。测试：`python -m unittest discover -s sdks/python -v`。
