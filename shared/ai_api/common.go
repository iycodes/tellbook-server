package aiapi

type MessageChannel string

const (
	MessageChannelSMS      MessageChannel = "sms"
	MessageChannelWhatsApp MessageChannel = "whatsapp"
	MessageChannelEmail    MessageChannel = "email"
	MessageChannelInApp    MessageChannel = "in_app"
)

type MessageTone string

const (
	MessageToneFriendly   MessageTone = "friendly"
	MessageToneWarm       MessageTone = "warm"
	MessageTonePolished   MessageTone = "polished"
	MessageToneDirect     MessageTone = "direct"
	MessageToneApologetic MessageTone = "apologetic"
)

type ContentTone string

const (
	ContentToneFriendly     ContentTone = "friendly"
	ContentToneWarm         ContentTone = "warm"
	ContentTonePolished     ContentTone = "polished"
	ContentToneDirect       ContentTone = "direct"
	ContentToneLuxury       ContentTone = "luxury"
	ContentToneConfident    ContentTone = "confident"
	ContentToneCalm         ContentTone = "calm"
	ContentToneMinimal      ContentTone = "minimal"
	ContentTonePlayful      ContentTone = "playful"
	ContentToneProfessional ContentTone = "professional"
)

type ContentLength string

const (
	ContentLengthShort  ContentLength = "short"
	ContentLengthMedium ContentLength = "medium"
	ContentLengthLong   ContentLength = "long"
)

type ContentGenerationMode string

const (
	ContentGenerationModeGenerate ContentGenerationMode = "generate"
	ContentGenerationModeImprove  ContentGenerationMode = "improve"
)

type ContentGenerationOptions struct {
	Tone          ContentTone   `json:"tone,omitempty"`
	Length        ContentLength `json:"length,omitempty"`
	Audience      string        `json:"audience,omitempty"`
	Keywords      []string      `json:"keywords,omitempty"`
	AvoidPhrases  []string      `json:"avoid_phrases,omitempty"`
	ReferenceText string        `json:"reference_text,omitempty"`
	Context       []NamedValue  `json:"context,omitempty"`
}

type MessageTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type NamedValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
