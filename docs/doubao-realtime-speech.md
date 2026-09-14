# 豆包实时语音接入

1.0.7 将实时语音作为独立能力加入运行时：`speech` 是厂商无关的长连接会话契约，
`adapter/speech/doubao` 负责豆包 JSON WebSocket 协议。它不实现 `model.Model`，也不进入
Agent 的普通 LLM 请求链路。

```text
Browser (WebSocket/WebRTC data)
        ⇅ PCM/控制事件
业务 Web 后端
        ⇅ speech.Session
Doubao realtime adapter
        ⇅ JSON WebSocket
豆包实时语音 3.0
        ⇢ tool.Registry（仅在 function call 时）
```

## 最小接入

```go
client, err := doubao.New(doubao.Config{
    APIKey: os.Getenv("DOUBAO_REALTIME_API_KEY"),
})
if err != nil { return err }

tools, err := speech.RegistryToolSet(registry, nil)
if err != nil { return err }

config := doubao.DefaultSession(
    "你是客服助手，回答简洁准确。",
    "zh_female_xiaohe_jupiter_bigtts",
)
config.Tools = tools.Definitions
config.Extension = map[string]any{
    "asr": map[string]any{"extra": map[string]any{}},
    "tts": map[string]any{"extra": map[string]any{}},
    "dialog": map[string]any{"extra": map[string]any{
        "enable_loudness_norm": true,
    }},
}

session, err := client.Open(requestContext, config)
if err != nil { return err }
defer session.Close(context.Background())
```

浏览器上行的 16 kHz、单声道、int16 小端 PCM 建议每 20ms 发送 640 字节：

```go
if err := session.SendAudio(ctx, browserAudioFrame); err != nil { return err }
```

后端接收循环只需要按统一事件转发。音频已经由适配器完成 Base64 解码：

```go
for {
    event, err := session.Recv(ctx)
    if err != nil { return err }
    switch event.Type {
    case speech.EventAudioDelta:
        // 直接作为二进制帧发给浏览器；默认是 24 kHz pcm_s16le。
        if err := browser.WriteBinary(event.Audio); err != nil { return err }
    case speech.EventTranscriptionDelta, speech.EventTranscriptionDone,
         speech.EventTextDelta, speech.EventTextDone:
        if err := browser.WriteJSON(event); err != nil { return err }
    case speech.EventFunctionCalls:
        // 同一事件内的多个调用会并行执行，并按 call_id 聚合后一次回传。
        if err := session.ExecuteFunctionCalls(ctx, tools.Executor, event.FunctionCalls...); err != nil {
            logger.Error("voice tool call", "error", err)
        }
    case speech.EventError:
        // ProviderError.Retryable=true 表示 5xx，可在会话边界新建连接。
        return event.ProviderError
    }
}
```

浏览器关闭麦克风时调用 `SetMuted(ctx, true)`，恢复时调用
`SetMuted(ctx, false)`；不要只停止发送音频，否则服务端可能因输入流超时释放连接。
打断当前回复使用 `CancelResponse`。主动播报使用 `Say`，干预当前回复使用
`ReplaceSpeech`。

## 函数调用与安全

`speech.RegistryToolSet` 复用现有 `tool.Registry`，因此参数 JSON Schema、输入输出校验、
超时和 streaming tool 聚合逻辑与 Agent 一致。工具 ID 会映射为豆包可接受的 wire name，
例如 `weather.current` 映射为 `weather_current`，执行时再精确映射回原工具。

默认情况下，声明了确认要求的工具不会在语音通话中执行。若业务已经在 Web 层完成身份与
授权校验，可传入显式 authorizer：

```go
tools, err := speech.RegistryToolSet(registry,
    func(ctx context.Context, spec tool.Spec, call speech.FunctionCall) error {
        return authorizeVoiceTool(ctx, currentUser, spec, call)
    },
)
```

工具执行失败时适配器仍会用相同 `call_id` 回传结构化错误，避免模型永久等待；同时
`ExecuteFunctionCalls` 返回聚合错误供业务日志和监控使用。

## 生命周期与生产建议

- 每个浏览器通话创建一个 `speech.Session`，不要跨用户复用。
- 输入 PCM 必须按真实时间节奏发送；不要批量高速灌入。
- 正常结束一定调用 `Close`，它会等待 `session.closed` 后再断开 WebSocket。
- 网络中断或可重试 5xx 后创建新会话。不要自动重放旧音频或旧函数调用。
- 保存 `session.ID()` 可用于豆包支持的历史会话接续；更新配置时传给 `Update`。
- `Session` 支持一个接收循环和多个并发发送方，所有 WebSocket 写入在内部串行化。

完整、无声卡依赖的 PCM 示例见 `examples/12-doubao-realtime`。
