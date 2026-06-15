package epub

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseXHTMLDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectedNodes []*ContentNode
	}{
		{
			name: "single paragraph extracted",
			input: `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <body>
    <p>Hello world</p>
  </body>
</html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/p[1]", Text: "Hello world"},
			},
		},
		{
			name: "multiple paragraphs get stable indexed paths",
			input: `<html><body>
  <p>First</p>
  <p>Second</p>
  <p>Third</p>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/p[1]", Text: "First"},
				{NodePath: "/html[1]/body[1]/p[2]", Text: "Second"},
				{NodePath: "/html[1]/body[1]/p[3]", Text: "Third"},
			},
		},
		{
			name: "headings are extracted",
			input: `<html><body>
  <h1>Chapter Title</h1>
  <p>Body text.</p>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/h1[1]", Text: "Chapter Title"},
				{NodePath: "/html[1]/body[1]/p[1]", Text: "Body text."},
			},
		},
		{
			name: "empty paragraphs are skipped",
			input: `<html><body>
  <p>   </p>
  <p>Real content</p>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/p[2]", Text: "Real content"},
			},
		},
		{
			name: "paragraphs inside sections get correct paths",
			input: `<html><body>
  <section>
    <p>Inside section</p>
  </section>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/section[1]/p[1]", Text: "Inside section"},
			},
		},
		{
			name: "script and style tags are skipped",
			input: `<html><head><style>body{}</style></head><body>
  <script>var x = 1;</script>
  <p>Visible text</p>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/p[1]", Text: "Visible text"},
			},
		},
		{
			name: "inline markup inside paragraph: text is concatenated",
			input: `<html><body>
  <p>Some <em>emphasized</em> text.</p>
</body></html>`,
			expectedNodes: []*ContentNode{
				{NodePath: "/html[1]/body[1]/p[1]", Text: "Some emphasized text."},
			},
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := parseXHTMLDocument([]byte(tt.input))

			require.NoError(t, err)
			require.NotNil(t, doc)
			require.NotNil(t, doc.Root)
			require.Len(t, doc.Nodes, len(tt.expectedNodes))

			for i, want := range tt.expectedNodes {
				got := doc.Nodes[i]
				require.Equal(t, want.NodePath, got.NodePath, "node %d path", i)
				require.Equal(t, want.Text, got.Text, "node %d text", i)
				require.NotNil(t, got.Node, "node %d should reference a live html node", i)
			}
		})
	}
}
