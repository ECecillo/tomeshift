package translate

// Provider identifiers accepted by NewProvider / the EPUB_TRANSLATOR_NMT_PROVIDER
// config.
const (
	ProviderHTTP           = "http"           // our own {text,source_lang,target_lang} dev contract
	ProviderLibreTranslate = "libretranslate" // self-hosted LibreTranslate / Argos
	ProviderGoogle         = "google"         // Google Cloud Translation v2 (Basic)
)

// RequiresEndpoint reports whether a provider needs an explicit endpoint.
// Cloud Translation has one public host, so the Google adapter supplies it and
// the setting is optional there; the self-hosted engines have nowhere to
// default to. Callers use this to fail before a run rather than at the first
// segment.
func RequiresEndpoint(provider string) bool {
	return provider != ProviderGoogle
}

// RequiresAPIKey reports whether a provider cannot translate without
// credentials. Cloud Translation authenticates every call with an API key; the
// self-hosted engines accept one but do not need it.
//
// It is the symmetric half of RequiresEndpoint, and it is checked in the same
// place for the same reason: when the provider is built. Validating it at
// configuration load instead made every command need credentials — including
// `estimate`, whose whole purpose is to price a book with zero provider calls,
// and which would now demand a secret from the operator's password manager to
// do arithmetic.
func RequiresAPIKey(provider string) bool {
	return provider == ProviderGoogle
}

// NewProvider builds the NMT Translator for the configured provider. Unknown
// providers fall back to the default HTTP adapter so a typo never disables
// translation silently — callers should validate the value if strictness is
// required.
func NewProvider(provider, endpoint, apiKey string) Translator {
	switch provider {
	case ProviderGoogle:
		return NewGoogleTranslator(endpoint, apiKey)
	case ProviderLibreTranslate:
		return NewLibreTranslator(endpoint, apiKey)
	default:
		return NewHTTPTranslator(endpoint, apiKey)
	}
}
