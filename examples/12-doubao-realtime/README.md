# Doubao realtime speech

Send raw 16 kHz mono signed-16-bit little-endian PCM and save the returned
24 kHz PCM:

```bash
export DOUBAO_REALTIME_API_KEY="..."
export DOUBAO_REALTIME_VOICE="zh_female_xiaohe_jupiter_bigtts"
go run ./examples/12-doubao-realtime input.pcm output.pcm
```

The example also exposes the existing typed `clock.now` tool to the realtime
model and executes function calls through `speech.RegistryToolSet`.
