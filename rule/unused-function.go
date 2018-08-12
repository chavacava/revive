package rule

import (
	"fmt"
	"github.com/mgechev/revive/lint"
	"go/ast"
	"strings"
	"sync"
)

/*
Notes:
	1. Make type checking optional?
	2. Support methods
	3. Avoid passing a mutex at config.go
*/

// failureCandidate defines a potential failure
type failureCandidate struct {
	file   *lint.File
	f      *ast.FuncDecl
	usedBy []*ast.FuncDecl
	m      sync.Mutex
}

func (fc *failureCandidate) isUsedBy(f *ast.FuncDecl) bool {
	for _, user := range fc.usedBy {
		if f == user {
			return true
		}
	}

	return false
}

func (fc *failureCandidate) isUsed() bool {
	return len(fc.usedBy) != 0
}

func (fc *failureCandidate) addUser(f *ast.FuncDecl) {
	if fc.isUsedBy(f) {
		return
	}
	fc.m.Lock()
	fc.usedBy = append(fc.usedBy, f)
	fc.m.Unlock()
}

// UnusedFunctionRule checks for unused (private) functions and methods
type UnusedFunctionRule struct {
	M sync.Mutex
}

// map[pkg]->map[functionName]failureCandidate{}
var failureCandidates map[*lint.Package]map[string]*failureCandidate = map[*lint.Package]map[string]*failureCandidate{}

func (r *UnusedFunctionRule) addFailureCandidate(pkg *lint.Package, fName string, fc *failureCandidate) {
	r.M.Lock()
	if _, ok := failureCandidates[pkg]; !ok {
		failureCandidates[pkg] = map[string]*failureCandidate{}
	}
	pkgFuncs := failureCandidates[pkg]
	if _, ok := pkgFuncs[fName]; !ok {
		pkgFuncs[fName] = &failureCandidate{}
	}
	fCandidate := pkgFuncs[fName]
	fCandidate.f = fc.f
	fCandidate.file = fc.file
	fCandidate.usedBy = append(fCandidate.usedBy, fc.usedBy...)
	r.M.Unlock()
}

func (r *UnusedFunctionRule) getFailureCandidate(pkg *lint.Package, fName string) *failureCandidate {
	r.M.Lock()
	pkgFuncs, ok := failureCandidates[pkg]

	if ok {
		_, ok = pkgFuncs[fName]
	}
	r.M.Unlock()

	if !ok {
		r.addFailureCandidate(pkg, fName, &failureCandidate{})
	}

	r.M.Lock()
	defer r.M.Unlock()
	return failureCandidates[pkg][fName]

}

func (r *UnusedFunctionRule) addUseToFailureCandidate(pkg *lint.Package, fName string, usedBy *ast.FuncDecl) {
	fc := r.getFailureCandidate(pkg, fName)
	fc.addUser(usedBy)
}

// Apply applies the rule to given file.
func (r *UnusedFunctionRule) Apply(file *lint.File, _ lint.Arguments) []lint.Failure {
	v := lintUnusedFunction{r: r, file: file}
	file.Pkg.TypeCheck()
	ast.Walk(v, file.AST)

	var failures []lint.Failure
	return failures
}

// Name returns the rule name.
func (r *UnusedFunctionRule) Name() string {
	return "unused-function"
}

// Reduce (implements Reducer interface)
func (r *UnusedFunctionRule) Reduce(p *lint.Package) []lint.PkgLevelFailure {
	result := []lint.PkgLevelFailure{}

	pkgFuncs, ok := failureCandidates[p]
	if !ok {
		return result
	}

	for k, v := range pkgFuncs {
		if v.isUsed() || v.f == nil {
			continue
		}

		result = append(result, lint.PkgLevelFailure{File: v.file, Failure: lint.Failure{
			Confidence: 1,
			Node:       v.f,
			Category:   "style",
			Failure:    fmt.Sprintf("Function %s is unused", k),
		}})
	}

	return result
}

type lintUnusedFunction struct {
	r    *UnusedFunctionRule
	file *lint.File
}

func (v lintUnusedFunction) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		mustAddAsCandidate := !ast.IsExported(n.Name.Name) && n.Name.Name != "main" && n.Name.Name != "init" && n.Recv == nil
		if mustAddAsCandidate {
			v.r.addFailureCandidate(v.file.Pkg, n.Name.Name, &failureCandidate{file: v.file, f: n, usedBy: []*ast.FuncDecl{}})
		}

		if n.Body == nil {
			return v
		}

		fbw := functionBodyWalker{pkg: v.file.Pkg, f: n, r: v.r}
		ast.Walk(fbw, n.Body)
		return nil
	case *ast.FuncLit:
		if n.Body == nil {
			return v
		}

		fbw := functionBodyWalker{pkg: v.file.Pkg, f: &ast.FuncDecl{}, r: v.r}
		ast.Walk(fbw, n.Body)
		return nil
	}

	return v
	// TODO handle methods!
}

type functionBodyWalker struct {
	pkg *lint.Package
	f   *ast.FuncDecl
	r   *UnusedFunctionRule
}

func (v functionBodyWalker) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.CallExpr:
		id, ok := n.Fun.(*ast.Ident)
		if !ok {
			return v // ignore calls of type pkgname.funcName
		}

		fc := v.r.getFailureCandidate(v.pkg, id.Name)
		fc.addUser(v.f)
	case *ast.Ident:
		o, ok := v.pkg.TypesInfo.Uses[n]
		if !ok {
			return v
		}

		typeName := o.Type().Underlying().String()

		if strings.HasPrefix(typeName, "func(") {
			fc := v.r.getFailureCandidate(v.pkg, n.Name)
			fc.addUser(v.f)
		}
	}

	return v
}
