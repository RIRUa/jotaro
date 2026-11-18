// Package mcpserver is the MCP server behind "jotaro mcp". It helps write jotaro scenarios and read saved results.
//
// It serves the authoring guide, the threshold syntax and a guide to reading results as resources, and offers tools to check
// a scenario (syntax, a few jotaro-specific lint rules that match jotaro's types, type check by compiling), to validate
// threshold expressions, and to summarize a saved result file and judge it against thresholds.
// It talks to the client over stdio and never runs a scenario: it only checks code and reads result files.
package mcpserver

import (
	"context"
	"embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed content/*.md
var content embed.FS

// Run serves MCP over stdio until the client disconnects or ctx is done.
//
// version is the version of the running binary, such as "v0.1.3", or "dev" (or empty) for a build whose version is unknown.
// It is reported to the client as the server version, and it selects the jotaro version that check_scenario type-checks
// against when no dir is given, so the code is checked against the API the embedded guide describes.
func Run(ctx context.Context, version string) error {
	return newServer(version).Run(ctx, &mcp.StdioTransport{})
}

// newServer builds the server with all resources, tools and prompts registered.
func newServer(version string) *mcp.Server {
	if version == "" {
		version = "dev"
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "jotaro", Version: version}, &mcp.ServerOptions{
		Instructions: "Helps write jotaro (Go HTTP load-testing library) scenarios. " +
			"Read the jotaro://guide resource first, write a complete `package main` file, " +
			"then call check_scenario and fix the diagnostics until it passes. " +
			"After the user has run the scenario and saved a JSON result, call summarize_result to read it " +
			"(jotaro://result explains the numbers) and check_result to judge it against thresholds. Do not read the result file directly; it is large.",
	})
	addResources(s)
	addTools(s, version)
	addPrompts(s)
	return s
}
