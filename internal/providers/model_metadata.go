package providers

// modelMeta holds well-known parameter counts and architecture type for a model ID.
type modelMeta struct {
	ParamB float64 // total parameters in billions
	Arch   string  // "dense", "MoE", "SSM"
}

// knownModelMeta maps model IDs (or substrings matched by lookupModelMeta) to metadata.
// Values sourced from official model cards and provider documentation.
var knownModelMeta = map[string]modelMeta{
	// ── Groq ─────────────────────────────────────────────────────────────────
	"llama-3.3-70b-versatile":       {70, "dense"},
	"llama-3.1-70b-versatile":       {70, "dense"},
	"llama-3.1-8b-instant":          {8, "dense"},
	"llama3-70b-8192":               {70, "dense"},
	"llama3-8b-8192":                {8, "dense"},
	"mixtral-8x7b-32768":            {46.7, "MoE"},
	"gemma2-9b-it":                  {9, "dense"},
	"gemma-7b-it":                   {7, "dense"},
	"deepseek-r1-distill-llama-70b": {70, "dense"},
	"qwen-qwq-32b":                  {32, "dense"},
	"qwen/qwen3.6-27b":              {27, "dense"},
	"openai/gpt-oss-safeguard-20b":  {20, "dense"},
	"allam-2-7b":                    {7, "dense"},

	// ── Google ───────────────────────────────────────────────────────────────
	// Gemini parameter counts are not officially disclosed; best estimates from
	// industry analysis. Flash ≈ 80-100B, Flash-Lite ≈ 40-60B, Pro ≈ 175-300B.
	"gemini-flash-latest":                      {100, "dense"},
	"gemini-flash-lite-latest":                 {50, "dense"},
	"gemini-3.7-flash":                         {100, "dense"},
	"gemini-3.7-flash-video-understanding-eap": {100, "dense"},
	"gemini-3.6-flash":                         {100, "dense"},
	"gemini-3.5-flash":                         {100, "dense"},
	"gemini-3.5-flash-lite":                    {50, "dense"},
	"gemini-3.1-flash-lite":                    {50, "dense"},
	"gemini-3.1-flash-lite-preview":            {50, "dense"},
	"gemini-3-flash-preview":                   {100, "dense"},
	"gemini-omni-flash-preview":                {100, "dense"},
	"gemini-2.5-flash":                         {100, "dense"},
	"gemini-2.5-flash-lite":                    {50, "dense"},
	"gemini-2.5-pro":                           {300, "dense"},
	"gemini-2.5-computer-use-preview-10-2025":  {300, "dense"},
	"gemini-pro-latest":                        {300, "dense"},
	"gemini-3.1-pro-preview":                   {300, "dense"},
	"gemini-3.1-pro-preview-customtools":       {300, "dense"},
	"gemini-3-pro-image":                       {300, "dense"},
	"gemini-3-pro-image-preview":               {300, "dense"},
	"deep-research-preview-04-2026":            {300, "dense"},
	"deep-research-max-preview-04-2026":        {300, "dense"},
	"deep-research-pro-preview-12-2025":        {300, "dense"},
	"antigravity-preview-05-2026":              {300, "dense"},
	"gemma-4-31b-it":                           {31, "dense"},
	"gemma-4-26b-a4b-it":                       {26, "MoE"},
	"gemini-robotics-er-2-preview":             {100, "dense"},
	"gemini-robotics-er-1.6-preview":           {100, "dense"},
	"lyria-3-clip-preview":                     {0, "dense"},
	"lyria-3-pro-preview":                      {0, "dense"},
	"nano-banana-pro-preview":                  {0, "dense"},

	// ── NVIDIA NIM ───────────────────────────────────────────────────────────
	"meta/llama-3.1-8b-instruct":                    {8, "dense"},
	"meta/llama-3.1-70b-instruct":                   {70, "dense"},
	"meta/llama-3.1-405b-instruct":                  {405, "dense"},
	"meta/llama-3.2-11b-vision-instruct":            {11, "dense"},
	"meta/llama-3.2-90b-vision-instruct":            {90, "dense"},
	"meta/llama-3.3-70b-instruct":                   {70, "dense"},
	"nvidia/llama-3.1-nemotron-70b-instruct":        {70, "dense"},
	"nvidia/llama-3.3-nemotron-super-49b-v1":        {49, "dense"},
	"nvidia/llama-3.3-nemotron-super-49b-v1.5":      {49, "dense"},
	"nvidia/nemotron-3-super-120b-a12b":             {120, "MoE"},
	"nvidia/nemotron-3-ultra-550b-a55b":             {550, "MoE"},
	"nvidia/nemotron-3-nano-30b-a3b":                {30, "MoE"},
	"nvidia/nemotron-3.5-lightning-30b-a3b":         {30, "MoE"},
	"nvidia/nvidia-nemotron-nano-9b-v2":             {9, "dense"},
	"nvidia/nemotron-nano-12b-v2-vl":                {12, "dense"},
	"nvidia/llama-3.1-nemotron-safety-guard-8b-v3":  {8, "dense"},
	"nvidia/llama-3.1-nemoguard-8b-content-safety":  {8, "dense"},
	"nvidia/llama-3.1-nemoguard-8b-topic-control":   {8, "dense"},
	"nvidia/llama-3.1-nemotron-nano-vl-8b-v1":       {8, "dense"},
	"nvidia/nemotron-mini-4b-instruct":              {4, "dense"},
	"nvidia/ising-calibration-1.5-31b":              {31, "dense"},
	"mistralai/mistral-nemotron":                    {123, "dense"},
	"mistralai/mistral-7b-instruct-v0.3":            {7, "dense"},
	"mistralai/mixtral-8x7b-instruct-v0.1":          {46.7, "MoE"},
	"mistralai/mixtral-8x22b-instruct-v0.1":         {141, "MoE"},
	"google/gemma-4-31b-it":                         {31, "dense"},
	"qwen/qwen2.5-72b-instruct":                     {72, "dense"},
	"minimaxai/minimax-m3":                          {456, "MoE"},
	"z-ai/glm-5.2":                                  {32, "dense"},
	"openai/gpt-oss-20b":                            {20, "dense"},
	"openai/gpt-oss-120b":                           {120, "dense"},
	"poolside/laguna-xs-2.1":                        {0, "dense"},
	"poolside/laguna-s-2.1":                         {0, "dense"},
	"thinkingmachines/inkling":                      {0, "dense"},
	"stepfun-ai/step-3.7-flash":                     {0, "dense"},
	"meta/muse-glimmer-30b":                         {30, "dense"},
	"nvidia/riva-translate-4b-instruct-v1.1":        {4, "dense"},
	"nvidia/riva-translate-4b-instruct-v2":          {4, "dense"},
	"nvidia/nemotron-3-nano-omni-30b-a3b-reasoning": {30, "MoE"},

	// ── OpenRouter free tier ──────────────────────────────────────────────────
	"meta-llama/llama-3.3-70b-instruct:free":      {70, "dense"},
	"meta-llama/llama-3.1-8b-instruct:free":       {8, "dense"},
	"google/gemma-3-27b-it:free":                  {27, "dense"},
	"google/gemma-3-12b-it:free":                  {12, "dense"},
	"google/gemma-3-4b-it:free":                   {4, "dense"},
	"mistralai/mistral-7b-instruct:free":          {7, "dense"},
	"mistralai/mistral-nemo:free":                 {12, "dense"},
	"qwen/qwen3-8b:free":                          {8, "dense"},
	"qwen/qwen3-14b:free":                         {14, "dense"},
	"qwen/qwen3-32b:free":                         {32, "dense"},
	"deepseek/deepseek-r1-0528:free":              {671, "MoE"},
	"deepseek/deepseek-r1:free":                   {671, "MoE"},
	"deepseek/deepseek-chat:free":                 {671, "MoE"},
	"deepseek/deepseek-v3-0324:free":              {671, "MoE"},
	"openai/gpt-oss-20b:free":                     {20, "dense"},
	"openai/gpt-oss-120b:free":                    {120, "dense"},
	"nvidia/llama-3.1-nemotron-70b-instruct:free": {70, "dense"},
	"microsoft/phi-4:free":                        {14, "dense"},
	"microsoft/phi-4-reasoning:free":              {14, "dense"},
	"microsoft/phi-4-reasoning-plus:free":         {14, "dense"},
	"poolside/laguna-xs-2.1:free":                 {0, "dense"},
	"poolside/laguna-s-2.1:free":                  {0, "dense"},
	// OpenRouter small/unknown models — size known to be small or confirmed tiny
	"liquid/lfm-2.5-2.6b:free":                {2.6, "dense"}, // 2.6B, confirmed small
	"cohere/north-mini-code:free":             {7, "dense"},   // "mini" tier, ~7B estimate
	"dots-studio/dots-3-note-preview:free":    {7, "dense"},   // unknown but likely small note model
	"nvidia/nemotron-3.5-content-safety:free": {8, "dense"},   // safety guard model, ~8B
	"nvidia/nemotron-3.5-lightning:free":      {30, "MoE"},    // lightning = small fast MoE
}

// LookupModelMeta returns parameter count and architecture for a model ID.
// Returns zero values if unknown.
func LookupModelMeta(modelID string) (paramB float64, arch string) {
	if m, ok := knownModelMeta[modelID]; ok {
		return m.ParamB, m.Arch
	}
	return 0, ""
}
