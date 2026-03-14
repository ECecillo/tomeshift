package epub

type RawEPUB struct {
	Mimetype   []byte
	Container  []byte // META-INF/container.xml
	OPFPath    string
	OPFContent []byte
	Files      map[string][]byte // All files by path
}

type Book struct {
	Path      string
	Manifest  Manifest
	Spine     []SpineItem
	Contents  map[string]*XHTMLDocument // href -> parsed doc
	Resources map[string][]byte         // Non-XHTML files
}

type Manifest struct {
	Items map[string]ManifestItem
}

type ManifestItem struct {
	ID         string
	Href       string
	MediaType  string
	Properties []string
}

type SpineItem struct {
	IDRef string
}

type XHTMLDocument struct{}
