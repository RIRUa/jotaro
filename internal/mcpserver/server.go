// Package mcpserver is the MCP server behind "jotaro mcp". It helps write jotaro scenarios.
//
// It serves the authoring guide and the threshold syntax as resources, and offers tools to check
// a scenario (syntax, a few jotaro-specific lint rules, type check by compiling) and to validate
// threshold expressions. It talks to the client over stdio and never runs the scenario it checks.
package mcpserver

import (
	"context"
	"embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed content/*.md
var content embed.FS

// Run serves MCP over stdio until the client disconnects or ctx is done.
func Run(ctx context.Context) error {
	return newServer().Run(ctx, &mcp.StdioTransport{})
}

// newServer builds the server with all resources, tools and prompts registered.
func newServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "jotaro", Version: "v0.1.0"}, &mcp.ServerOptions{
		Instructions: "Helps write jotaro (Go HTTP load-testing library) scenarios. " +
			"Read the jotaro://guide resource first, write a complete `package main` file, " +
			"then call check_scenario and fix the diagnostics until it passes.",
	})
	addResources(s)
	addTools(s)
	addPrompts(s)
	return s
}
