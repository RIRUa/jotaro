package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// docResources lists the embedded documents served as resources.
var docResources = []struct {
	uri, file, name, description string
}{
	{
		uri:         "jotaro://guide",
		file:        "content/guide.md",
		name:        "guide",
		description: "How to write a jotaro scenario: skeleton, API overview, load models (Users / Arrival) and a checklist.",
	},
	{
		uri:         "jotaro://thresholds",
		file:        "content/thresholds.md",
		name:        "thresholds",
		description: "Threshold syntax (p95<200ms, error_rate<1%, p99{step=login}<100ms) and the available metrics.",
	},
}

// addResources registers the embedded documents as markdown resources.
func addResources(s *mcp.Server) {
	for _, d := range docResources {
		s.AddResource(&mcp.Resource{
			URI:         d.uri,
			Name:        d.name,
			Description: d.description,
			MIMEType:    "text/markdown",
		}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			b, err := content.ReadFile(d.file)
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "text/markdown",
				Text:     string(b),
			}}}, nil
		})
	}
}
