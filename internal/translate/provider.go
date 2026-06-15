package translate

// Provider identifiers accepted by NewProvider / the EPUB_TRANSLATOR_NMT_PROVIDER
// config.
const (
	ProviderHTTP           = "http"           // our own {text,source_lang,target_lang} dev contract
	ProviderLibreTranslate = "libretranslate" // self-hosted LibreTranslate / Argos
)

// NewProvider builds the NMT Translator for the configured provider. Unknown
// providers fall back to the default HTTP adapter so a typo never disables
// translation silently — callers should validate the value if strictness is
// required.
func NewProvider(provider, endpoint, apiKey string) Translator {
	switch provider {
	case ProviderLibreTranslate:
		return NewLibreTranslator(endpoint, apiKey)
	default:
		return NewHTTPTranslator(endpoint, apiKey)
	}
}
