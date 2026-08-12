# Usage Examples

Minimal, copy-paste examples for talking to a running HarnessClaw Engine over
WebSocket. The full wire contract lives in
[protocols/websocket.md](protocols/websocket.md) (Protocol v2, card model) —
this page only covers the happy path to get a first conversation going.

## 1. Start the engine

```bash
make run        # builds config + starts with configs/config.yaml
```

By default the WebSocket channel listens on port **8081** at path **`/v1/ws`**
(`channels.websocket.port` in `configs/config.yaml`).

## 2. Smoke-test with wscat

```bash
npm install -g wscat
wscat -c ws://localhost:8081/v1/ws
```

Right after the upgrade the server pushes a `session.event` with
`kind=opened` — that is the handshake. Then send a message:

```json
{"type":"user.message","event_id":"c1","content":[{"type":"text","text":"Say hello"}]}
```

You will see a stream of `card.*` events: `card.add` opens a card,
`card.append` streams content into it, `card.close` ends it with metrics
(duration, tokens, cost).

## 3. Minimal Node.js client

```js
// npm install ws
import WebSocket from "ws";

const ws = new WebSocket("ws://localhost:8081/v1/ws");

ws.on("open", () => {
  ws.send(JSON.stringify({
    type: "user.message",
    event_id: "c1",
    content: [{ type: "text", text: "List the top-level directories, then summarize the project." }],
  }));
});

// keep-alive: a bare ping frame every 30s (no envelope, no seq)
const keepAlive = setInterval(() => ws.send('{"type":"ping"}'), 30_000);

ws.on("message", (raw) => {
  const evt = JSON.parse(raw.toString());

  switch (evt.type) {
    case "pong":
      return;

    case "session.event":
      if (evt.payload?.kind === "opened") console.error("[session opened]");
      return;

    case "card.append":
      // streaming text: accumulate per (card_id, channel, index)
      if (evt.payload.channel === "text") process.stdout.write(evt.payload.chunk);
      return;

    case "card.close":
      if (evt.envelope.card_kind === "turn") {
        console.error(`\n[turn done] ${evt.metrics?.tokens_out ?? "?"} tokens out`);
        clearInterval(keepAlive);
        ws.close();
      }
      return;

    case "prompt.user": {
      // the engine is asking for permission (e.g. to run a tool)
      const { request_id, kind } = evt.payload;
      if (kind === "permission") {
        ws.send(JSON.stringify({
          type: "prompt.user_response",
          request_id,
          decision: "approved",
          payload: { approved: true, scope: "once", message: "" },
        }));
      }
      return;
    }
  }
});
```

What to expect, in order:

1. `session.event` (`kind=opened`) — handshake complete
2. `card.add` for the turn, then message/tool cards as the engine works
3. `card.append` frames carrying streamed text (`channel: "text"`) or tool
   input JSON (`channel: "tool_input"`)
4. Possibly `prompt.user` — answer it with `prompt.user_response`, echoing the
   `request_id`; unanswered prompts block the turn
5. `card.close` with `metrics` when each card (and finally the turn) finishes

## 4. Interrupting a turn

Send the trace ID you observed in the envelopes:

```json
{"type":"session.interrupt","trace_id":"tr_xxx"}
```

## 5. Answering the common prompt kinds

| `payload.kind` | Reply `decision` | Reply `payload` |
|---|---|---|
| `permission` | `approved` / `denied` | `{"approved":true,"scope":"once","message":""}` |
| `question` | `approved` / `denied` | `{"selected_options":["yes"],"custom_text":""}` |
| `plan_review` | `approved` / `denied` | `{"approved":true,"updated_steps":[],"reason":""}` |
| `step_decision` | `continue` / `retry` / `cancel` | `{"note":""}` |

Every reply must echo the prompt's `request_id`. The server accepts the first
answer only; duplicates come back as an error event.

## 6. Reconnecting

Reconnect with the **same `session_id`** and resume the event stream:

```json
{"type":"session.resume","trace_id":"tr_xxx","last_seq":42}
```

The server replays events from `last_seq + 1`. If the server restarted while a
prompt was unanswered, it re-sends that prompt (same `request_id`) right after
`session.event(kind=opened)` — deduplicate by `request_id` instead of showing a
second dialog. See
[protocols/websocket.md](protocols/websocket.md) §2.4 for the full recovery
contract.