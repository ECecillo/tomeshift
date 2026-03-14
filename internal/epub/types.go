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

type (
	Manifest      struct{}
	SpineItem     struct{}
	XHTMLDocument struct{}
)
