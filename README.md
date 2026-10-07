# Decision Gateway PoC

> **This is a proof of concept.** It tests one idea: when migrating a **legacy chatbot**, put a **decision gateway** in front of it. Every incoming LINE webhook message is sent to the gateway first, which decides **which function or feature should handle it**. Fixed features (hours, price, job status, contact…) go to their own handler; anything open-ended goes to an AI. The decision is made by **Rethink**, a model-free decision engine from [katgpt](https://github.com/katopz/katgpt-rs).

```
                      ┌─────────────────────────┐   intent: hours   ─► hours handler
LINE ─► POST /webhook ─►   DECISION GATEWAY      ├─► intent: price   ─► price handler
                      │  (Rethink)              ├─► intent: status  ─► status handler
                      │  which feature is this? ├─► … contact, menu, greeting, thanks
                      └─────────────────────────┘
                                 │ not sure / open-ended (abstain)
                                 └──────────────────────────────────► AI (Gemini)
```

In this PoC the "features" are canned handlers in `internal/bot`. In a real migration they would be the legacy chatbot's existing functions or services, and the gateway would be the single entry point that routes to them.

## Why a decision gateway

A legacy chatbot usually matches keywords or menu paths. The gateway replaces that front door with a decision that is:

- **Explicit.** One label per message (an intent, or `ai`), plus a confidence, logged for every message.
- **Able to abstain.** When the engine isn't sure, the message escalates to the AI instead of getting a wrong canned answer. That is the costly failure here.
- **Cheap and fast.** The decision runs in-process in well under a millisecond, with no per-call cost.
- **Measurable.** Every decision is stored, so accuracy, latency and cost can be compared against alternatives.

## Credit: the Rethink pattern comes from katgpt

The decision pattern in this PoC is **Rethink**, from **[katopz/katgpt-rs](https://github.com/katopz/katgpt-rs)**. All credit for the pattern goes to that project and its author; please read the original. The parts this PoC borrows:

1. **Modelless decisions.** Answer from a labelled corpus by nearest-neighbour similarity instead of calling a model. katgpt's `CorpusDistanceGate` (max-cosine to registered exemplars) is the closest named piece, described in its Research 576.
2. **Calibrated confidence.** Turn raw similarity into a probability with a small two-parameter (Platt-style) fit learned from outcomes, with no model weights changed. katgpt's `SigmoidGateCalibrator` (Issue 810, Research 562) is the named piece.
3. **Abstain instead of guess.** When confidence is low, the engine says "not sure" and the message escalates to a heavier path (here, the AI). Research 562 argues this is exactly what a forced-answer decision model can't do.
4. **Fast path first, slow path on doubt.** A cheap local decision handles the common cases; deliberation is only paid for when needed.

Related katgpt research notes: `Research 562` (the decision-model landscape and the abstain argument) and `Research 576` ("Laya: the Open Jev Generalist", the open model used here as the comparison decider).

**What is and isn't from katgpt.** katgpt's `riir-rethink` implementation is a separate repository that I did not have access to. `internal/decision/rethink.go` is **my own small Go implementation of the pattern as katgpt documents it publicly**: character 2/3-gram cosine similarity over a labelled corpus, a Platt calibration fitted leave-one-out, and abstain below a threshold. It is not katgpt's code and may differ from it. The approach is theirs; any bugs here are mine.

Other credits: [Convai Innovations](https://huggingface.co/convaiinnovations/laya) (Laya), and [Vercel AI Gateway](https://vercel.com/docs/ai-gateway/modalities/decision) (hosting for the decision models).

## What the PoC compares

| | Rethink (the proposal) | Laya (the alternative) |
|---|---|---|
| What | Local, modelless nearest-neighbour engine | A hosted decision model, called through Vercel AI Gateway (`POST /v1/evaluate`) |
| How it decides | Cosine over char 2/3-grams against a labelled corpus, Platt-calibrated `p = σ(A·sim + B)`, **abstains** below a threshold | One `choice` question over the intents + `ai`; returns probabilities |
| Cost | Free, in-process | Free, but rate-limited to 5 req/min on Vercel's free tier |
| Latency | ~0.9 µs per decision (was ~38 µs before the optimisation, same answers) | ~0.5–1.4 s |

A keyword matcher runs alongside as a baseline. Each message is then answered by a canned handler or by Gemini.

Whichever decider is "driving" answers the user. The others run in the background on the same message, so all of them are scored on identical traffic. `DECIDER_MODE=swap` alternates the driver every `SWAP_INTERVAL` for a live A/B.

### Results so far (early, small samples)

- **Held-out eval (24 labelled messages):** Rethink 96% accuracy, 0 AI questions wrongly sent to a canned handler, ~1 µs, $0. Keyword baseline 71%.
- **Play test (16 real LINE messages, judged by hand):** Rethink looked right on 5/5; Laya on about 4/10 (it read "สวัสดี" as thanks, and gave an opening-hours reply to an unrelated question at 0.69 confidence).
- **Caveats, please read:**
  - Rethink's corpus and the eval set were written by the same person and share phrasing, so Rethink has an advantage on this kind of test. Rethink needs a labelled corpus; the hosted models need none.
  - Samples are tiny and Laya's prompt was changed mid-test. This is evidence to follow up on, not a verdict.

## Performance notes: how Rethink went from 37.8 µs to 0.9 µs

The first version of `internal/decision/rethink.go` took **37.8 µs and 79 allocations** per decision. After profiling it takes **~0.9 µs and 1 allocation** (about 40× faster) with identical decisions. These are the patterns that were slow, and what replaced them. They apply to any hot path that runs on every message.

| Slow pattern | Replaced with | Notes |
|---|---|---|
| Scoring the message against **every** example, with map lookups | An **inverted index** built once (n-gram → examples containing it); a query only touches examples that share a gram with it | the largest single win |
| **Strings as map keys** (built a string per character pair/triple) | Pack the characters into one **integer key** | removes most allocations |
| **Maps built per call** (counts, votes) | **Pooled buffers** (`sync.Pool`) and tiny fixed arrays for top-K and votes | less garbage, so less GC |
| `sort.Slice` over all scores to get the top few | A small **top-K insertion array** | |
| `fmt.Sprintf` for the reason text (the biggest cost left, ~0.7 µs) | `strconv.Append*`, integer maths for 2-decimal numbers | output is byte-identical |
| Quoting and truncating the matched example on **every call** | Do it **once at startup** | |
| `unicode.IsLetter` / `ToLower` per character | A **lookup table** for the common range (Latin, Thai); the slow path only for rare characters | |
| Go `map` for the hot lookup | A small **open-addressing hash table** | |

**Method.** Profile first (`go test -bench . -benchmem -cpuprofile cpu.prof`, then `go tool pprof -top`); the allocation count was the quickest sign of waste. Pin behaviour before optimising: `internal/decision/rethink_golden_test.go` stores 97 decisions plus the fitted calibration, so a speedup can't silently change answers. The one intended difference is that an empty message now returns `ai` instead of an arbitrary label (both route to the AI).

Reproduce: `go test ./internal/decision -run xxx -bench Rethink -benchmem`.

**Don't do this by default.** The original code was clearer, and 38 µs is irrelevant next to a ~1 s hosted-model call. Optimise only code that runs on every message or item, and only with a measurement showing it matters.

## Run

```bash
cp .env.example .env              # fill in keys, see below
docker compose up -d              # MongoDB + gateway; dashboard http://localhost:8080/
# or, without Docker: go run .    # (memory-only stats unless MONGO_URI is set)
go test ./...
```

Without any keys it still runs: Rethink decides, AI replies are placeholders, and the dashboard plus `POST /api/simulate` work locally.

MongoDB (one container) stores raw webhook events, every handled message with all deciders' verdicts, AI call logs and eval runs. The dashboard rebuilds its numbers from it, so they survive restarts and can be filtered by time range.

**Dashboard (`/`)** has a head-to-head table, a live A/B of what users actually got, per-decider stats, Gemini tokens and cost, an AI call log, and a recent-message table showing every decider's verdict. *Run eval* scores all deciders on the 24 held-out messages. It has no login by default; set `DASHBOARD_PASSWORD` before sharing the URL.

## Credentials

### Vercel AI Gateway (Laya)
1. Vercel dashboard → **AI Gateway** → **API Keys** → *Create key* → `AI_GATEWAY_API_KEY=...`
2. `JEV_MODEL=convaiinnovations/laya` (free, 5 req/min).

### Gemini
`GEMINI_API_KEY=...` from https://aistudio.google.com/apikey (default model `gemini-2.5-flash`; check `GEMINI_PRICE_*` against current prices).

### LINE
1. https://developers.line.biz/console/ → create a Provider and a **Messaging API** channel.
2. **Basic settings** → **Channel secret** → `LINE_CHANNEL_SECRET=...`
3. **Messaging API** tab → **Channel access token (long-lived)** → *Issue* → `LINE_CHANNEL_ACCESS_TOKEN=...`
4. Expose the server over HTTPS, e.g. `ngrok http 8080`.
5. **Messaging API** tab → **Webhook URL** = `https://<your-host>/webhook` → *Verify* → turn on **Use webhook**.
6. LINE Official Account Manager → **Response settings** → turn **off** *Auto-response* and *Greeting message*.
7. Rich menu (one button per feature; each tap has a known correct answer, so it scores the decider):
   ```bash
   go run . richmenu preview menu.png   # optional
   go run . richmenu create             # renders, uploads, sets as default
   go run . richmenu list | delete <id>
   ```
8. Add the bot as a friend and tap the menu.

## Config (`.env`)

| Var | Default | |
|---|---|---|
| `DECIDER_MODE` | `swap` | `swap`, `jev` (Laya drives) or `rethink` |
| `SWAP_INTERVAL` | `1m` | Go duration |
| `RETHINK_MIN_CONFIDENCE` | `0.65` | Rethink abstains below this calibrated confidence |
| `JEV_MIN_CONFIDENCE` | `0.6` | Laya's pick below this escalates to AI |
| `JEV_URL` / `JEV_MODEL` | `https://ai-gateway.vercel.sh/v1/evaluate` / `convaiinnovations/laya` | env names are historical; the slot runs any decision model |
| `JEV_MAX_RPM` | `0` | cap on calls per minute; over the cap falls back to Rethink |
| `JEV_TIMEOUT_MS` | `3000` | timeout → Rethink fallback |
| `MONGO_URI` / `MONGO_DB` | `mongodb://localhost:27017` / `decision_gateway` | unset = memory only |
| `DASHBOARD_USER` / `DASHBOARD_PASSWORD` | `team` / unset | password for everything except `/webhook` |

## Layout

```
main.go                       server, webhook, rich menu CLI
dashboard.html                embedded dashboard
internal/decision/            Decider interface; rethink.go, jev.go (the hosted-model client, runs Laya), keyword.go;
                              pipeline.go (drive + fallback + shadow), ratelimit.go,
                              corpus.go (corpus + eval set), eval.go
internal/bot/                 the canned "features" (stand-ins for legacy handlers)
internal/gemini/              Gemini responder with token/cost accounting
internal/line/                signature check, reply, rich menu API
internal/richmenu/            menu buttons (with known answers) + image renderer
internal/store/               MongoDB persistence
internal/stats/               aggregation behind /api/stats
```

To make Rethink smarter, add real chat lines to `decision.Corpus`. Keep `EvalSet` disjoint from it so the eval stays honest.
