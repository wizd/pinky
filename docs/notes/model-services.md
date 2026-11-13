# Model services: the presets Ask Pinky offers

Settings › Agents › Ask Pinky on the computer, and Pinky's Settings on Android and iPhone, offer a list of model services: each has an API style, an address and the models to suggest. The list is `services` in `internal/pilot/services.go`, the only copy. `go generate ./internal/pilot` writes it as JSON for the phone apps (`android/companion/app/src/main/assets/model_services.json`, `ios/app/App/ModelServices.json`), since their settings screens open while the core may not run; `TestServicesFiles` fails when a file is out of date.

Everything below was checked against the service's own documentation on 2026-11-13. Pinky speaks two APIs: Chat Completions (`<base_url>/chat/completions`, `Authorization: Bearer`) and the Messages API (`<base_url>/v1/messages`). It sends `tool_choice: "auto"` on Chat Completions and never forces a tool, which is what every service below accepts (several refuse `required` or a named tool while thinking, or altogether).

## What a preset holds

- `id`: the service in settings and in usage, and the name its key is saved under (`KeyName`). The two custom entries (`anthropic-compatible`, `openai-compatible`) keep the key names of before there were services, `anthropic` and `openai`, so nobody's saved key is lost.
- `region`: `china` or `global` for a service with a mainland China address and an international one. Their keys are not interchangeable (Qwen, Kimi, SiliconFlow and Zhipu each say so), so each region is its own entry with its own key.
- `models`: the suggested models, the default first. Each reads images and calls tools through Chat Completions, as its service documents. The person may type any other.
- `key_optional`, `local`: Ollama and LM Studio run on the person's computer and need no key unless they set one up. On a phone their address is the computer's on the network, which Settings says.
- `priced_models`: the models whose prices Pinky knows on that service (`internal/pilot/price.go`), by the start of their names. With one of them the cost limit and usage are in dollars; otherwise in tokens.

Settings saved before there were services map to one (`ServiceFor`): Anthropic or OpenAI at their own address, or the custom entry for an address the person typed, even a service's own address, so the key saved for it still applies. One change in behaviour: OpenAI with no address used to need a model typed; it is now the OpenAI preset with its default model.

## The services

| ID | API | Address | Suggested models | Prices |
|---|---|---|---|---|
| `anthropic` | Messages | `https://api.anthropic.com` (the default) | `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5` | USD |
| `openai` | Chat | `https://api.openai.com/v1` | `gpt-5.6-terra` | USD |
| `deepseek` | Chat | `https://api.deepseek.com` | `deepseek-flash` | USD, peak and off-peak |
| `gemini` | Chat | `https://generativelanguage.googleapis.com/v1beta/openai` | `gemini-4-hydrogen`, `gemini-4-carbon` | USD, Carbon doubles on 2026-12-01 |
| `kimi-cn` / `kimi-intl` | Chat | `https://api.moonshot.cn/v1` / `https://api.moonshot.ai/v1` | `kimi-k3.1-pro`, `kimi-k3.1-pro-highspeed`, `kimi-k3` | not verified |
| `openrouter` | Chat | `https://openrouter.ai/api/v1` | `anthropic/claude-sonnet-5.5`, `google/gemini-4-hydrogen` | vary by provider |
| `qwen-cn` / `qwen-intl` | Chat | `https://dashscope.aliyuncs.com/compatible-mode/v1` / `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `qwen3.7-plus` | USD |
| `siliconflow-cn` / `siliconflow-intl` | Chat | `https://api.siliconflow.cn/v1` / `https://api.siliconflow.com/v1` | none (the person names one) | not mapped |
| `volcengine-ark` | Chat | `https://ark.cn-beijing.volces.com/api/v3` | `doubao-seed-2-1-pro-260915`, `doubao-seed-2-1-lite-260915`, `doubao-seed-2-1-turbo-260628` | CNY only |
| `glm-cn` / `glm-intl` | Chat | `https://open.bigmodel.cn/api/paas/v4` / `https://api.z.ai/api/paas/v4` | `glm-5.3-flash` | USD on Z.ai only |
| `ollama` | Chat | `http://localhost:11434/v1` | none | n/a |
| `lm-studio` | Chat | `http://localhost:1234/v1` | none | n/a |
| `openai-compatible` | Chat | typed | typed | none |
| `anthropic-compatible` | Messages | typed | typed (default `claude-opus-5-5`) | Anthropic's, for Claude models |

Pinky speaks Chat Completions to every service that has it, even those with an Anthropic-style endpoint too (DeepSeek, Qwen, Kimi, Zhipu, SiliconFlow, OpenRouter, Ollama, LM Studio): the Chat endpoints are the documented, general ones, and the Anthropic ones differ per service in auth (several take only Bearer, which the custom Anthropic entry sends anyway) and in what they accept. A person who wants one of those can still use the Anthropic-compatible entry with its address.

### Anthropic

- Address and Messages API: standard. Models and prices: https://platform.claude.com/docs/en/about-claude/models/overview, https://platform.claude.com/docs/en/about-claude/pricing.
- Opus 5.5 $4 / $0.20 cache read / $20, 5-minute cache write $5; Sonnet 5.5 $2 / $0.10 / $10, write $2.50; Haiku 5.5 $0.10 / $0.01 / $0.50 up to 100K-token prompts, $0.50 / $0.05 / $2.50 above; Fable 5.2 $8 / $0.20 / $40.
- Its OpenAI compatibility layer is "not considered a long-term or production-ready solution" and refuses forced tool use for current models (https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk), so Pinky uses the Messages API.

### OpenAI

- https://developers.openai.com/api/docs/models, pricing https://developers.openai.com/api/docs/pricing.
- GPT-6.1 Sol and GPT-6.1 Terra: "Tool calling requires the Responses API" (https://developers.openai.com/api/docs/models/gpt-6.1-sol, https://developers.openai.com/api/docs/models/gpt-6.1-terra); the function-calling guide says the same of GPT-6 Astra (https://developers.openai.com/api/docs/guides/function-calling). GPT-6 Luna calls functions on Chat Completions only with `reasoning_effort: "none"` (https://developers.openai.com/api/docs/models/gpt-6-luna).
- So the preset suggests `gpt-5.6-terra`, which supports Chat Completions, function calling and images with no such restriction (https://developers.openai.com/api/docs/models/gpt-5.6-terra), $2 / $0.20 / $12. Prices of the GPT-6 models are in the table for a person who types one (Astra $10 / $1 / $50, 6.1 Sol $2 / $0.10 / $10, 6.1 Terra $1 / $0.05 / $5, Luna $0.10 / $0.01 / $0.50; prompts over 272K tokens cost more, Pinky's stay far below).

### DeepSeek

- https://api-docs.deepseek.com/, models and prices https://api-docs.deepseek.com/quick_start/pricing, Chat Completions https://api-docs.deepseek.com/api/create-chat-completion.
- `deepseek-flash` (V4.1 Flash) reads images; `deepseek-v4-pro` does not, so it is priced but not suggested.
- `required` and named `tool_choice` return 400 in thinking mode, the default; Pinky sends `auto`.
- Prices, peak (01:00–04:00 and 06:00–10:00 UTC, Monday to Friday) and off-peak at half: Flash $0.30 / $0.006 / $1.20 at peak, V4 Pro $1.32 / $0.044 / $3.96. Chinese public holidays are off-peak, which Pinky does not know: it counts them as peak, so it may overstate cost slightly.

### Google Gemini

- OpenAI compatibility (still in beta): https://aistudio.google.com/docs/openai. Models: https://aistudio.google.com/docs/models, https://aistudio.google.com/docs/models/gemini-4-hydrogen, https://aistudio.google.com/docs/models/gemini-4-carbon. Pricing: https://aistudio.google.com/docs/pricing.
- Hydrogen $1.50 / $0.075 / $7.50; Carbon and Argon $2 / $0.10 / $10 through 2026-11-30, then $4 / $0.20 / $20 (Pinky switches on that date); 3.5 Flash-Lite $0.30 / $0.03 / $2.50.

### Kimi (Moonshot)

- International https://platform.kimi.ai/docs/api/overview.md, China https://platform.kimi.com/docs/api/overview.md; keys of the two platforms "are not interchangeable" (https://platform.kimi.ai/docs/api/list-models.md). Models: https://platform.kimi.ai/docs/models.md (K3.1 Pro, its highspeed variant and K3 read images; K2 is closed to new users).
- Prices not verified: the pricing pages render client-side. Usage is in tokens.

### OpenRouter

- https://openrouter.ai/docs/quickstart, tool calling https://openrouter.ai/docs/guides/features/tool-calling. Model pages: https://openrouter.ai/anthropic/claude-sonnet-5.5, https://openrouter.ai/google/gemini-4-hydrogen.
- Prices vary by the provider OpenRouter routes to, so Pinky counts tokens.

### Qwen (Alibaba Cloud Model Studio)

- Addresses https://www.alibabacloud.com/help/en/model-studio/base-url (keys are per region), models https://www.alibabacloud.com/help/en/model-studio/models, function calling https://www.alibabacloud.com/help/en/model-studio/qwen-function-calling (no `required`), pricing https://www.alibabacloud.com/help/en/model-studio/model-pricing.
- Only `qwen3.7-plus` is listed under visual understanding, so it is the one suggested. Singapore $0.4 / $1.6, Beijing $0.276 / $1.101 (up to 256K input); Max and Flash priced too. Cache hits cost a tenth of the input (the general rule there).
- Coding Plan and Token Plan keys are for coding tools only and are not offered.

### SiliconFlow

- International https://docs.siliconflow.com/en/api-reference/chat-completions/chat-completions.md, China https://docs.siliconflow.cn/cn/userguide/guides/function-calling. Keys work only on the site that issued them.
- Which models read images and call tools is not written down per model, so the preset suggests none. Its pricing page names models by display name, not API ID, so no prices are mapped.

### Volcengine Ark

- https://docs.volcengine.com/docs/82379/1330626; the model field takes a model ID (no endpoint ID needed), tools and images https://docs.volcengine.com/docs/82379/1494384; models https://docs.volcengine.com/docs/82379/1330310 (each suggested one reads images and calls functions).
- Prices are in CNY only (https://docs.volcengine.com/docs/82379/1544106), so Pinky counts tokens.

### Zhipu GLM (Z.ai / BigModel)

- Z.ai https://docs.z.ai/guides/overview/quick-start, BigModel https://docs.bigmodel.cn/cn/guide/start/quick-start. Function calling "only supports `auto`" (https://docs.z.ai/guides/capabilities/function-calling).
- `glm-5.3` reads no images (https://docs.z.ai/guides/llm/glm-5.3); `glm-5.3-flash` does (https://docs.z.ai/guides/vlm/glm-5.3-flash), so it is the one suggested.
- Z.ai prices (https://docs.z.ai/guides/overview/pricing): GLM-5.3 $1.4 / $0.26 / $4.4, Flash $0.15 / $0.03 / $0.50, FlashX $0.37 / $0.075 / $1.25. BigModel's CNY prices render client-side: not verified.
- The Coding Plan endpoint is for coding tools only and is not offered.

### Ollama and LM Studio

- Ollama https://docs.ollama.com/api/openai-compatibility: the local server ignores the key; `tool_choice` is unsupported (Pinky's `auto` is the default anyway); images as base64 only, which is how Pinky sends them.
- LM Studio https://lmstudio.ai/docs/developer/openai-compat, tools https://lmstudio.ai/docs/developer/openai-compat/tools, auth (off by default) https://lmstudio.ai/docs/developer/core/authentication.

## Check on save

Saving a key, a model or an address checks it (`pilot.Check`; Settings also has Check now): one request with one tool, `ready`, and the instruction to call it, about 300 tokens. It reports whether the model answered with a tool call, else a problem the screens say in the person's language: no model, no key, bad key (401, 403), no credit (402), not found (404), busy (429, 5xx), refused, no tools (answered in words, or an error about tools), unreachable, or an answer Pinky could not read. The service's own words go with it, never the key (`redact`). Desktop runs it in the app; the phones send `{"cmd":"check", key, service, …}` to the core's control socket, which uses the key once and keeps no copy. A check's tokens count in usage.

## Usage

`pilot.Usage` counts what the model used, by the device's day, service and model (the address's host for the custom entries): requests, input, output, cache reads and writes, the cost of what had known prices and the tokens without. It is `model-usage.json` in the app's data directory (mode 0600), kept 180 days (`UsageKeepDays`), never sent anywhere. Desktop reads it through `PilotUsage`, the phones through the control socket's `usage` command (`since`, a day). Settings shows today, the last 7 and 30 days and the last 7 days by service and model; the task box shows today's.

Chat Completions usage is read from `prompt_tokens_details.cached_tokens`. A service that reports cache hits under another field (DeepSeek may) has all its input counted at the input price, which overstates the cost a little. Not verified against a live response.

## Left open

- Kimi, BigModel, SiliconFlow and Volcengine prices in a form Pinky can use (USD by API ID).
- The Responses API, for OpenAI's GPT-6 flagships with tools.
- Links to each service's key page in Settings.
