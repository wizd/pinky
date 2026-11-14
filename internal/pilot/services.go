package pilot

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// Service is a model service Settings offers: the API it speaks, its
// address and the models to suggest. Desktop Pinky reads Services; the
// phone apps read the same list from a file generated from it
// (ServicesJSON, `go generate ./internal/pilot`), since their settings
// screens open while the core may not run. Every address and API style
// here was checked against the service's own documentation
// (docs/notes/model-services.md cites them).
type Service struct {
	// ID names the service in settings and in usage. It is also the name
	// its API key is saved under (KeyName), except for the two custom
	// entries, whose keys keep the names they had before there were
	// services.
	ID string `json:"id"`
	// Name is the service's own name, in no language in particular.
	Name string `json:"name"`
	// Region tells two entries of one service apart: RegionChina for its
	// mainland China address, RegionGlobal for the international one;
	// empty when it has one. Settings says it in the person's language.
	Region string `json:"region,omitempty"`
	// API is Anthropic or OpenAI.
	API string `json:"api"`
	// BaseURL is the API's address, as Config.BaseURL takes it; empty for
	// the custom entries, whose address the person types, and for
	// Anthropic, whose address is the default.
	BaseURL string `json:"base_url"`
	// Models are the models to suggest, the default first; the person may
	// type another. Empty when the service has no models of its own to
	// name (a local server runs what the person downloaded).
	Models []ServiceModel `json:"models,omitempty"`
	// KeyOptional is a server on the person's own computer, which needs
	// no key unless they set one up.
	KeyOptional bool `json:"key_optional,omitempty"`
	// Custom is an entry for an address the person types.
	Custom bool `json:"custom,omitempty"`
	// Local is a server that usually runs on the person's computer; on a
	// phone its address is the computer's on the network, not localhost.
	Local bool `json:"local,omitempty"`
	// PricedModels are the models whose prices Pinky knows on this
	// service, by the start of their names, as priceOf matches them; with
	// one of them the cost limit is in dollars. Filled in by Services.
	PricedModels []string `json:"priced_models,omitempty"`
}

// ServiceModel is a model a service offers.
type ServiceModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"` // as the service names it; empty is ID
}

// Label is the model as the person reads it.
func (m ServiceModel) Label() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// The regions of Service.Region.
const (
	RegionChina  = "china"
	RegionGlobal = "global"
)

// The IDs of the two entries for an address the person types. Their keys
// are saved under the API's name, as all keys were before there were
// services (KeyName).
const (
	CustomOpenAI    = "openai-compatible"
	CustomAnthropic = "anthropic-compatible"
)

// services is the list Settings offers, in its order: Anthropic and
// OpenAI first, then the others by name, local servers, then the custom
// entries. Every service here speaks its API with Pinky's tools and
// takes screenshots as images, as its documentation says; each suggested
// model reads images and calls tools through the API Pinky speaks to it:
// the Responses API for OpenAI's GPT-6 models, Chat Completions for the
// rest (apis.go). All of it as of 2026-11-14.
var services = []Service{
	{
		ID: Anthropic, Name: "Anthropic", API: Anthropic,
		Models: []ServiceModel{
			{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
			{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
			{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5"},
		},
	},
	{
		// GPT-6 Astra, 6.1 Sol and 6.1 Terra call tools only through the
		// Responses API, and GPT-6 Luna on Chat Completions only without
		// reasoning, so Pinky speaks Responses to the GPT-6 models and
		// Chat Completions to the others (usesResponses). GPT-5.6 Terra,
		// on Chat Completions, stays the default: Responses has not been
		// run against a live key yet, and a person who never chose a
		// model keeps the one they have.
		ID: OpenAI, Name: "OpenAI", API: OpenAI, BaseURL: DefaultOpenAIURL,
		Models: []ServiceModel{
			{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
			{ID: "gpt-6.1-terra", Name: "GPT-6.1 Terra"},
			{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol"},
		},
	},
	{
		// DeepSeek V4 Pro reads no images.
		ID: "deepseek", Name: "DeepSeek", API: OpenAI, BaseURL: "https://api.deepseek.com",
		Models: []ServiceModel{{ID: "deepseek-flash"}},
	},
	{
		ID: "gemini", Name: "Google Gemini", API: OpenAI, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		Models: []ServiceModel{
			{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
			{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		},
	},
	{
		ID: "kimi-cn", Name: "Kimi", Region: RegionChina, API: OpenAI, BaseURL: "https://api.moonshot.cn/v1",
		Models: kimiModels,
	},
	{
		ID: "kimi-intl", Name: "Kimi", Region: RegionGlobal, API: OpenAI, BaseURL: "https://api.moonshot.ai/v1",
		Models: kimiModels,
	},
	{
		ID: "openrouter", Name: "OpenRouter", API: OpenAI, BaseURL: "https://openrouter.ai/api/v1",
		Models: []ServiceModel{
			{ID: "anthropic/claude-sonnet-5.5"},
			{ID: "google/gemini-4-hydrogen"},
		},
	},
	{
		// Only Qwen 3.7 Plus of Model Studio's models is said to read
		// images.
		ID: "qwen-cn", Name: "Qwen", Region: RegionChina, API: OpenAI, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models: qwenModels,
	},
	{
		ID: "qwen-intl", Name: "Qwen", Region: RegionGlobal, API: OpenAI, BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		Models: qwenModels,
	},
	{
		// Which of its models call tools and read images is not written
		// down: the person names one.
		ID: "siliconflow-cn", Name: "SiliconFlow", Region: RegionChina, API: OpenAI, BaseURL: "https://api.siliconflow.cn/v1",
	},
	{
		ID: "siliconflow-intl", Name: "SiliconFlow", Region: RegionGlobal, API: OpenAI, BaseURL: "https://api.siliconflow.com/v1",
	},
	{
		ID: "volcengine-ark", Name: "Volcengine Ark", API: OpenAI, BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Models: []ServiceModel{
			{ID: "doubao-seed-2-1-pro-260915"},
			{ID: "doubao-seed-2-1-lite-260915"},
			{ID: "doubao-seed-2-1-turbo-260628"},
		},
	},
	{
		// GLM-5.3 reads no images.
		ID: "glm-cn", Name: "Zhipu GLM", Region: RegionChina, API: OpenAI, BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		Models: glmModels,
	},
	{
		ID: "glm-intl", Name: "Zhipu GLM", Region: RegionGlobal, API: OpenAI, BaseURL: "https://api.z.ai/api/paas/v4",
		Models: glmModels,
	},
	{ID: "ollama", Name: "Ollama", API: OpenAI, BaseURL: "http://localhost:11434/v1", KeyOptional: true, Local: true},
	{ID: "lm-studio", Name: "LM Studio", API: OpenAI, BaseURL: "http://localhost:1234/v1", KeyOptional: true, Local: true},
	{ID: CustomOpenAI, Name: "OpenAI-compatible", API: OpenAI, Custom: true},
	{ID: CustomAnthropic, Name: "Anthropic-compatible", API: Anthropic, Custom: true},
}

var (
	kimiModels = []ServiceModel{
		{ID: "kimi-k3.1-pro"},
		{ID: "kimi-k3.1-pro-highspeed"},
		{ID: "kimi-k3"},
	}
	qwenModels = []ServiceModel{{ID: "qwen3.7-plus"}}
	glmModels  = []ServiceModel{{ID: "glm-5.3-flash"}}
)

// Services returns the services Settings offers, in its order.
func Services() []Service {
	out := make([]Service, len(services))
	for i, s := range services {
		s.Models = append([]ServiceModel(nil), s.Models...)
		s.PricedModels = pricedModels(s)
		out[i] = s
	}
	return out
}

// ServiceByID returns a service of Services.
func ServiceByID(id string) (Service, bool) {
	s, ok := lookup(id)
	if ok {
		s.Models = append([]ServiceModel(nil), s.Models...)
		s.PricedModels = pricedModels(s)
	}
	return s, ok
}

// lookup is ServiceByID without the copies and PricedModels.
func lookup(id string) (Service, bool) {
	for _, s := range services {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// ServiceFor is the service of settings saved before there were services:
// Anthropic's or OpenAI's own at the API's default address, else the
// custom entry for that API. An address the person typed stays theirs,
// with the key saved for it, even when it is a service's own.
func ServiceFor(provider, baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.ToLower(strings.TrimSpace(provider)) == OpenAI {
		if base == "" || base == DefaultOpenAIURL {
			if _, ok := lookup(OpenAI); ok {
				return OpenAI
			}
		}
		return CustomOpenAI
	}
	if base == "" {
		return Anthropic
	}
	return CustomAnthropic
}

// KeyName is the name the API key of a service is saved under
// (secret.ModelKeyAccount on a computer, the phone apps' own stores): its
// ID, or the API's name for the custom entries and for no service.
func KeyName(service, provider string) string {
	switch service {
	case CustomOpenAI:
		return OpenAI
	case CustomAnthropic:
		return Anthropic
	case "":
		if strings.ToLower(strings.TrimSpace(provider)) == OpenAI {
			return OpenAI
		}
		return Anthropic
	}
	return service
}

// host is the host of an address, for usage of a custom entry.
func host(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

//go:generate go run ./genservices

// ServicesFiles are the files, from the repository's root, that hold
// ServicesJSON for the phone apps.
var ServicesFiles = []string{
	"android/companion/app/src/main/assets/model_services.json",
	"ios/app/App/ModelServices.json",
}

// servicesFile is the file the phone apps read: ServicesJSON.
type servicesFile struct {
	Comment  string    `json:"//"`
	Services []Service `json:"services"`
}

// ServicesJSON is Services as the phone apps read it, in the files `go
// generate` writes (gen_services.go).
func ServicesJSON() []byte {
	f := servicesFile{
		Comment:  "Generated from internal/pilot/services.go by `go generate ./internal/pilot`. Do not edit.",
		Services: Services(),
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		panic(err)
	}
	return b.Bytes()
}
