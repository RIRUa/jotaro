package mcpserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// Diagnostic is one finding about a scenario file.
type Diagnostic struct {
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Source is where the finding came from: "syntax", "lint" or "build".
	Source  string `json:"source"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message"`
}

// lintScenario parses src and applies the jotaro-specific rules.
//
// A syntax error is returned as the only diagnostic, since the rules need a syntax tree.
func lintScenario(src string) []Diagnostic {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scenario.go", src, parser.SkipObjectResolution)
	if err != nil {
		return syntaxDiagnostics(err)
	}
	l := &linter{fset: fset}
	l.checkPlans(f)
	l.checkSteps(f)
	l.checkUncheckedDo(f)
	l.checkRawClient(f)
	return l.diags
}

type linter struct {
	fset  *token.FileSet
	diags []Diagnostic
}

func (l *linter) add(sev, rule string, pos token.Pos, msg string) {
	p := l.fset.Position(pos)
	l.diags = append(l.diags, Diagnostic{Severity: sev, Source: "lint", Rule: rule, Line: p.Line, Column: p.Column, Message: msg})
}

// checkPlans verifies every jotaro.Plan literal: exactly one of Users, UserStages and Arrival, and a Duration.
func (l *linter) checkPlans(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isPlanType(lit.Type) {
			return true
		}
		keys := map[string]bool{}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					keys[id.Name] = true
				}
			}
		}
		models := 0
		for _, k := range []string{"Users", "UserStages", "Arrival"} {
			if keys[k] {
				models++
			}
		}
		switch {
		case models > 1:
			l.add("error", "plan-load-model", lit.Pos(), "Plan sets more than one of Users, UserStages and Arrival; specify exactly one (Users / UserStages = closed model, Arrival = open model)")
		case models == 0:
			l.add("error", "plan-load-model", lit.Pos(), "Plan sets none of Users, UserStages and Arrival; specify exactly one")
		}
		if !keys["Duration"] {
			l.add("error", "plan-duration", lit.Pos(), "Plan.Duration is not set; Run rejects a non-positive Duration")
		}
		if !keys["Scenario"] {
			l.add("error", "plan-scenario", lit.Pos(), "Plan.Scenario is not set")
		}
		return true
	})
}

// isPlanType reports whether t is jotaro.Plan (or Plan, for a dot import or the package itself).
func isPlanType(t ast.Expr) bool {
	switch t := t.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && x.Name == "jotaro" && t.Sel.Name == "Plan"
	case *ast.Ident:
		return t.Name == "Plan"
	}
	return false
}

// checkSteps warns about the same literal step name used twice in one function, since the report
// would merge the two steps into one row.
func (l *linter) checkSteps(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncDecl:
			body = fn.Body
		case *ast.FuncLit:
			body = fn.Body
		}
		if body == nil {
			return true
		}
		seen := map[string]bool{}
		// Names used inside nested function literals belong to those literals, except the
		// closures passed to Step itself, which are part of this function's flow.
		ast.Inspect(body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := stepName(call)
			if !ok {
				return true
			}
			if seen[name] {
				l.add("warning", "duplicate-step-name", call.Pos(), "step name "+strconv.Quote(name)+" is used more than once in this function; the report merges them into one step")
			}
			seen[name] = true
			return true
		})
		return false
	})
}

// stepName returns the literal name of a u.Step("name", ...) call.
func stepName(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Step" || len(call.Args) != 2 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

// checkUncheckedDo warns about a request whose response is dropped, as in `s.Get("/x").Do()`.
func (l *linter) checkUncheckedDo(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		es, ok := n.(*ast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Do" && len(call.Args) == 0 {
			l.add("warning", "unchecked-response", call.Pos(), "the response of Do() is dropped; finish the chain with ExpectStatus(code) or Decode(&v), or the status is never checked")
		}
		return true
	})
}

// checkRawClient notes requests sent through u.Client, which are not recorded in the report.
func (l *linter) checkRawClient(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Client" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "u" {
			l.add("warning", "unrecorded-request", sel.Pos(), "requests sent through u.Client are not recorded; use the request builders of the Step (s.Get / s.Post ...) to have them measured")
		}
		return true
	})
}
