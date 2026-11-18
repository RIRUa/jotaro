package mcpserver

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
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

// parseScenario parses src. A syntax error is returned as diagnostics, since the rules need a syntax tree.
func parseScenario(src string) (*token.FileSet, *ast.File, []Diagnostic) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scenario.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, syntaxDiagnostics(err)
	}
	return fset, f, nil
}

// lintScenario parses src and applies the jotaro-specific rules without type information (the syntactic fallback).
//
// Names are resolved from the file's imports (so an aliased import works, and a user's own Plan type is not mistaken
// for jotaro's), but what a variable's type is cannot be known, so everything it reports is a warning, and it can miss
// findings. A syntax error is returned as the only diagnostic.
func lintScenario(src string) []Diagnostic {
	fset, f, diags := parseScenario(src)
	if f == nil {
		return diags
	}
	return lintSyntactic(fset, f)
}

// lintSyntactic applies the rules to f using only the syntax tree. It reports warnings only.
func lintSyntactic(fset *token.FileSet, f *ast.File) []Diagnostic {
	l := &linter{fset: fset}
	l.scanImports(f)
	l.run(f)
	return l.diags
}

// lintTyped applies the rules to f using the type information in info, which must come from type-checking f.
// Objects are matched instead of names, so it neither misses code that uses an alias or another variable name nor
// flags a look-alike type. Findings that are certain are errors.
func lintTyped(fset *token.FileSet, f *ast.File, info *types.Info) []Diagnostic {
	l := &linter{fset: fset, info: info}
	l.run(f)
	return l.diags
}

// linter applies the rules to one file. With info set it matches objects; without, it matches names resolved
// from the file's imports.
type linter struct {
	fset  *token.FileSet
	info  *types.Info // nil for the syntactic fallback
	diags []Diagnostic

	// Syntactic fallback only.
	pkgNames  map[string]bool // the names the jotaro package is imported under
	dotImport bool            // the package is imported with a dot
	userVars  map[string]bool // names declared with type *jotaro.User
}

func (l *linter) run(f *ast.File) {
	l.checkPlans(f)
	l.checkSteps(f)
	l.checkUncheckedDo(f)
	l.checkRawClient(f)
}

// add records a finding. Without type information nothing is certain, so every finding is a warning.
func (l *linter) add(sev, rule string, pos token.Pos, msg string) {
	if l.info == nil {
		sev = "warning"
	}
	p := l.fset.Position(pos)
	l.diags = append(l.diags, Diagnostic{Severity: sev, Source: "lint", Rule: rule, Line: p.Line, Column: p.Column, Message: msg})
}

// scanImports finds the names the jotaro package is imported under, and the names declared with type *jotaro.User.
func (l *linter) scanImports(f *ast.File) {
	l.pkgNames = map[string]bool{}
	l.userVars = map[string]bool{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != jotaroModule {
			continue
		}
		switch {
		case imp.Name == nil:
			l.pkgNames["jotaro"] = true
		case imp.Name.Name == ".":
			l.dotImport = true
		case imp.Name.Name != "_":
			l.pkgNames[imp.Name.Name] = true
		}
	}
	declare := func(names []*ast.Ident, typ ast.Expr) {
		if l.isJotaroType(typ, "User", true) {
			for _, n := range names {
				l.userVars[n.Name] = true
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			declare(n.Names, n.Type)
		case *ast.ValueSpec:
			declare(n.Names, n.Type)
		}
		return true
	})
}

// isJotaroType reports whether t is the syntax of the jotaro type name: jotaro.Plan under whatever name the package
// is imported with, or a bare Plan when it is dot-imported. With ptr, one leading * is allowed.
func (l *linter) isJotaroType(t ast.Expr, name string, ptr bool) bool {
	if star, ok := t.(*ast.StarExpr); ok && ptr {
		t = star.X
	}
	switch t := t.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && l.pkgNames[x.Name] && t.Sel.Name == name
	case *ast.Ident:
		return l.dotImport && t.Name == name
	}
	return false
}

// isJotaroNamed reports whether t is the type jotaro.<name>, or a pointer to it.
func isJotaroNamed(t types.Type, name string) bool {
	if t == nil {
		return false
	}
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == name && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == jotaroModule
}

// isPlan reports whether lit is a literal of jotaro.Plan.
func (l *linter) isPlan(lit *ast.CompositeLit) bool {
	if l.info != nil {
		return isJotaroNamed(l.info.TypeOf(lit), "Plan")
	}
	return lit.Type != nil && l.isJotaroType(lit.Type, "Plan", false)
}

// method returns the jotaro type name and the method name of a call such as x.Do(), when type information says x
// is a jotaro type; otherwise "" and "".
func (l *linter) method(call *ast.CallExpr) (typ, name string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || l.info == nil {
		return "", ""
	}
	s := l.info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return "", ""
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != jotaroModule {
		return "", ""
	}
	for _, t := range []string{"Request", "User"} {
		if isJotaroNamed(s.Recv(), t) {
			return t, fn.Name()
		}
	}
	return "", ""
}

// builderMethods are the methods of *Step and *Request whose result is a *Request, which can be chained into Do.
var builderMethods = map[string]bool{
	"Request": true, "Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true,
	"JSON": true, "Body": true, "Form": true, "Query": true, "Queries": true,
	"Headers": true, "Header": true, "Bearer": true, "Basic": true,
}

// isRequestDo reports whether call is (*jotaro.Request).Do().
func (l *linter) isRequestDo(call *ast.CallExpr) bool {
	if len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Do" {
		return false
	}
	if l.info != nil {
		typ, name := l.method(call)
		return typ == "Request" && name == "Do"
	}
	// Without types, only a chain that visibly starts at a request builder (s.Get("/x").Header(...).Do()) is
	// recognised; a Do() on a variable could be any type's.
	for x := sel.X; ; {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return false
		}
		s, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || !builderMethods[s.Sel.Name] {
			return false
		}
		if _, chained := s.X.(*ast.CallExpr); !chained {
			return s.Sel.Name == "Request" || s.Sel.Name == "Get" || s.Sel.Name == "Post" ||
				s.Sel.Name == "Put" || s.Sel.Name == "Patch" || s.Sel.Name == "Delete"
		}
		x = s.X
	}
}

// isUserStep reports whether call is (*jotaro.User).Step(name, fn).
func (l *linter) isUserStep(call *ast.CallExpr) bool {
	if len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Step" {
		return false
	}
	if l.info != nil {
		typ, name := l.method(call)
		return typ == "User" && name == "Step"
	}
	// Without types, recognise a step whose function visibly takes a *jotaro.Step.
	fn, ok := call.Args[1].(*ast.FuncLit)
	if !ok || len(fn.Type.Params.List) != 1 {
		return false
	}
	return l.isJotaroType(fn.Type.Params.List[0].Type, "Step", true)
}

// isUserClient reports whether sel is the field (*jotaro.User).Client.
func (l *linter) isUserClient(sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != "Client" {
		return false
	}
	if l.info != nil {
		s := l.info.Selections[sel]
		if s == nil || s.Kind() != types.FieldVal {
			return false
		}
		v, ok := s.Obj().(*types.Var)
		return ok && v.Pkg() != nil && v.Pkg().Path() == jotaroModule
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && l.userVars[x.Name]
}

// key identifies the variable id refers to: its object with type information, its name without.
func (l *linter) key(id *ast.Ident) any {
	if id.Name == "_" {
		return nil
	}
	if l.info != nil {
		if obj := l.info.ObjectOf(id); obj != nil {
			return obj
		}
		return nil
	}
	return id.Name
}

// checkPlans verifies every jotaro.Plan literal: exactly one of Users, UserStages and Arrival, a Duration and a scenario.
//
// A Plan is often built in two steps: `plan := jotaro.Plan{...}` and then `plan.Users = 20`, often inside branches that pick
// the load model from a flag. So fields assigned later to the variable that holds the literal count as set, and once
// the load model is assigned that way the rule stays quiet about it, since which assignments run together cannot be told
// from the syntax. The one exception is a literal that already sets more than one model: that stays visible, as a warning
// when later assignments might undo it. A literal passed directly, with no variable, is judged by its fields alone.
func (l *linter) checkPlans(f *ast.File) {
	assigned := l.assignedFields(f)
	bound := l.boundPlans(f)
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !l.isPlan(lit) {
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
		var later map[string]bool
		if k := bound[lit]; k != nil {
			later = assigned[k]
		}
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

// assignedFields returns, per variable (see key), the fields assigned with `v.Field = ...` anywhere in the file.
// Without type information a variable is its name, so two variables of the same name in different functions are
// merged; the cost is a missed diagnostic at worst, never a false one.
func (l *linter) assignedFields(f *ast.File) map[any]map[string]bool {
	out := map[any]map[string]bool{}
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
			k := l.key(x)
			if k == nil {
				continue
			}
			if out[k] == nil {
				out[k] = map[string]bool{}
			}
			out[k][sel.Sel.Name] = true
		}
		return true
	})
	return out
}

// boundPlans maps a Plan literal to the variable it is assigned to (`plan := Plan{}`, `var plan = &Plan{}`).
// A literal that is not assigned to a plain variable has no entry.
func (l *linter) boundPlans(f *ast.File) map[*ast.CompositeLit]any {
	out := map[*ast.CompositeLit]any{}
	bind := func(name ast.Expr, value ast.Expr) {
		id, ok := name.(*ast.Ident)
		if !ok {
			return
		}
		if u, ok := value.(*ast.UnaryExpr); ok && u.Op == token.AND {
			value = u.X
		}
		if lit, ok := value.(*ast.CompositeLit); ok && l.isPlan(lit) {
			if k := l.key(id); k != nil {
				out[lit] = k
			}
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

// checkSteps warns about the same constant step name used twice in one function, since the report would merge the
// two steps into one row, and about a step name that is not a constant, since every distinct value becomes a row.
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
		// The closures passed to Step are part of the function's flow, so one walk covers the nested literals too.
		ast.Inspect(body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok || !l.isUserStep(call) {
				return true
			}
			name, constName := l.constString(call.Args[0])
			switch {
			case !constName:
				l.add("warning", "dynamic-step-name", call.Pos(), "the step name is not a constant; every distinct name becomes its own row in the report (and its own entry in the aggregator), so build it from data only when the set of values is small, and otherwise use one fixed name")
			case seen[name]:
				l.add("warning", "duplicate-step-name", call.Pos(), "step name "+strconv.Quote(name)+" is used more than once in this function; the report merges them into one step")
			}
			if constName {
				seen[name] = true
			}
			return true
		})
		return false
	})
}

// constString returns the value of e when it is a constant string: a literal, or with type information any constant
// expression such as a named constant.
func (l *linter) constString(e ast.Expr) (string, bool) {
	if l.info != nil {
		if tv, ok := l.info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
			return constant.StringVal(tv.Value), true
		}
		return "", false
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	return name, err == nil
}

const uncheckedMsg = "the response of Do() is never checked; finish the chain with an Expect check (ExpectStatus, ExpectStatusIn, ExpectHeader, ExpectBodyContains or ExpectJSON) or with Decode(&v), or the response is never checked"

// checkUncheckedDo warns about a request whose response is dropped, as in `s.Get("/x").Do()`, assigned to the blank
// identifier, or assigned to a variable that is never checked.
func (l *linter) checkUncheckedDo(f *ast.File) {
	uses := l.responseUses(f)
	// bound maps the variable a Do() result is assigned to the position of the first such call.
	bound := map[any]token.Pos{}
	bind := func(name ast.Expr, value ast.Expr) {
		call, ok := value.(*ast.CallExpr)
		if !ok || !l.isRequestDo(call) {
			return
		}
		id, ok := name.(*ast.Ident)
		if !ok {
			return // stored in a field or element: it escapes, so nothing can be said
		}
		if id.Name == "_" {
			l.add("warning", "unchecked-response", call.Pos(), uncheckedMsg)
			return
		}
		if k := l.key(id); k != nil {
			if _, dup := bound[k]; !dup {
				bound[k] = call.Pos()
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ExprStmt:
			if call, ok := n.X.(*ast.CallExpr); ok && l.isRequestDo(call) {
				l.add("warning", "unchecked-response", call.Pos(), uncheckedMsg)
			}
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
	for k, pos := range bound {
		if !uses[k] {
			l.add("warning", "unchecked-response", pos, uncheckedMsg)
		}
	}
}

// responseUses returns the variables that are used in a way that may check a response: anything but `_ = v` and
// v.JSON(path), which reads a value without looking at the status. A variable that is passed on, returned, read
// through a field (resp.Code) or used with Decode, Err or an Expect method counts as checked, so that the rule
// never flags code that does something with the response it cannot see through.
func (l *linter) responseUses(f *ast.File) map[any]bool {
	used := map[any]bool{}
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		id, ok := n.(*ast.Ident)
		if !ok || len(stack) < 2 {
			return true
		}
		k := l.key(id)
		if k == nil {
			return true
		}
		switch p := stack[len(stack)-2].(type) {
		case *ast.AssignStmt:
			for i, lhs := range p.Lhs {
				if lhs == ast.Expr(id) {
					return true // being assigned, not read
				}
				if len(p.Lhs) == len(p.Rhs) && p.Rhs[i] == ast.Expr(id) {
					if b, ok := lhs.(*ast.Ident); ok && b.Name == "_" {
						return true // `_ = resp` reads nothing
					}
				}
			}
		case *ast.ValueSpec:
			for _, name := range p.Names {
				if name == id {
					return true
				}
			}
		case *ast.SelectorExpr:
			if p.Sel == id {
				return true // a field or method name, not the variable
			}
			if p.Sel.Name == "JSON" {
				return true
			}
		}
		used[k] = true
		return true
	})
	return used
}

// checkRawClient notes requests sent through (*jotaro.User).Client, which are not recorded in the report.
func (l *linter) checkRawClient(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && l.isUserClient(sel) {
			l.add("warning", "unrecorded-request", sel.Pos(), "requests sent through the Client of a User are not recorded; use the request builders of the Step (s.Get / s.Post ...) to have them measured")
		}
		return true
	})
}
