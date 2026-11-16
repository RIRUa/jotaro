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

// checkPlans verifies every jotaro.Plan literal: exactly one of Users, UserStages and Arrival, a Duration and a scenario.
//
// A Plan is often built in two steps: `plan := jotaro.Plan{...}` and then `plan.Users = 20`, often inside branches that pick
// the load model from a flag. So fields assigned later to the variable that holds the literal count as set, and once
// the load model is assigned that way the rule stays quiet about it, since which assignments run together cannot be told
// from the syntax. The one exception is a literal that already sets more than one model: that stays visible, as a warning
// when later assignments might undo it. A literal passed directly, with no variable, is judged by its fields alone.
func (l *linter) checkPlans(f *ast.File) {
	assigned := assignedFields(f)
	bound := boundPlans(f)
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
		litModels := countModels(keys)
		// Fields assigned after the literal, through the variable that holds it.
		later := assigned[bound[lit]]
		laterModels := countModels(later)
		for k := range later {
			keys[k] = true
		}
		switch {
		case litModels > 1:
			sev := "error"
			if laterModels > 0 {
				sev = "warning" // a later assignment may have changed the model
			}
			l.add(sev, "plan-load-model", lit.Pos(), "Plan sets more than one of Users, UserStages and Arrival; specify exactly one (Users / UserStages = closed model, Arrival = open model)")
		case litModels == 0 && laterModels == 0:
			l.add("error", "plan-load-model", lit.Pos(), "Plan sets none of Users, UserStages and Arrival; specify exactly one (a field assigned later, such as plan.Users = 20, counts)")
		}
		if !keys["Duration"] {
			l.add("error", "plan-duration", lit.Pos(), "Plan.Duration is not set; Run rejects a non-positive Duration")
		}
		if !keys["Scenario"] && !keys["Scenarios"] {
			l.add("error", "plan-scenario", lit.Pos(), "neither Plan.Scenario nor Plan.Scenarios is set")
		}
		return true
	})
}

// countModels returns how many of Users, UserStages and Arrival are in keys.
func countModels(keys map[string]bool) int {
	n := 0
	for _, k := range []string{"Users", "UserStages", "Arrival"} {
		if keys[k] {
			n++
		}
	}
	return n
}

// assignedFields returns, per variable name, the fields assigned with `name.Field = ...` anywhere in the file.
// It goes by name only, so two variables of the same name in different functions are merged; the cost is a missed
// diagnostic at worst, never a false one.
func assignedFields(f *ast.File) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				continue
			}
			if out[x.Name] == nil {
				out[x.Name] = map[string]bool{}
			}
			out[x.Name][sel.Sel.Name] = true
		}
		return true
	})
	return out
}

// boundPlans maps a Plan literal to the name of the variable it is assigned to (`plan := Plan{}`, `var plan = &Plan{}`).
// A literal that is not assigned to a plain variable has no entry.
func boundPlans(f *ast.File) map[*ast.CompositeLit]string {
	out := map[*ast.CompositeLit]string{}
	bind := func(name ast.Expr, value ast.Expr) {
		id, ok := name.(*ast.Ident)
		if !ok {
			return
		}
		if u, ok := value.(*ast.UnaryExpr); ok && u.Op == token.AND {
			value = u.X
		}
		if lit, ok := value.(*ast.CompositeLit); ok && isPlanType(lit.Type) {
			out[lit] = id.Name
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i := range n.Lhs {
					bind(n.Lhs[i], n.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i := range n.Names {
					bind(n.Names[i], n.Values[i])
				}
			}
		}
		return true
	})
	return out
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
			l.add("warning", "unchecked-response", call.Pos(), "the response of Do() is dropped; finish the chain with an Expect check (ExpectStatus, ExpectStatusIn, ExpectHeader, ExpectBodyContains or ExpectJSON) or with Decode(&v), or the response is never checked")
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
