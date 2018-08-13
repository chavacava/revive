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
*/

// function represents functions of the linted source code
type function struct {
	file   *lint.File      // file where this function is defined
	node   *ast.FuncDecl   // ast node holding the declaration of this function
	usedBy []*ast.FuncDecl // declaration nodes of functions that use this function
	m      sync.Mutex
}

func (f *function) isUsedBy(u *ast.FuncDecl) bool {
	for _, user := range f.usedBy { // TODO can be optimized in performance
		if u == user {
			return true
		}
	}

	return false
}

func (f *function) isUsed() bool {
	return len(f.usedBy) != 0
}

func (f *function) addUser(u *ast.FuncDecl) {
	if f.isUsedBy(u) {
		return // do not add a duplicate entry in usedBy list
	}
	f.m.Lock()
	f.usedBy = append(f.usedBy, u)
	f.m.Unlock()
}

// UnusedFunctionRule checks for unused (private) functions and methods
type UnusedFunctionRule struct {
	m sync.Mutex
}

// map[pkg]->map[functionName]function
var functions map[*lint.Package]map[string]*function = map[*lint.Package]map[string]*function{}

func (r *UnusedFunctionRule) addFunction(pkg *lint.Package, fName string, fc *function) {
	r.m.Lock()
	if _, ok := functions[pkg]; !ok {
		functions[pkg] = map[string]*function{}
	}
	pkgFuncs := functions[pkg]
	if _, ok := pkgFuncs[fName]; !ok {
		pkgFuncs[fName] = &function{}
	}
	f := pkgFuncs[fName]
	f.node = fc.node
	f.file = fc.file
	f.usedBy = append(f.usedBy, fc.usedBy...)
	r.m.Unlock()
}

func (r *UnusedFunctionRule) getFunction(pkg *lint.Package, fName string) *function {
	r.m.Lock()
	pkgFuncs, ok := functions[pkg]

	if ok {
		_, ok = pkgFuncs[fName]
	}
	r.m.Unlock()

	if !ok {
		r.addFunction(pkg, fName, &function{})
	}

	r.m.Lock()
	defer r.m.Unlock()
	return functions[pkg][fName]

}

func (r *UnusedFunctionRule) addUseToFunction(pkg *lint.Package, fName string, usedBy *ast.FuncDecl) {
	fc := r.getFunction(pkg, fName)
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

	pkgFuncs, ok := functions[p]
	if !ok {
		return result
	}

	for k, v := range pkgFuncs {
		if v.isUsed() || v.node == nil {
			continue
		}

		result = append(result, lint.PkgLevelFailure{File: v.file, Failure: lint.Failure{
			Confidence: 0.8,
			Node:       v.node,
			Category:   "bad practice",
			Failure:    fmt.Sprintf("Function '%s' seems to be unused", k),
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
			v.r.addFunction(v.file.Pkg, n.Name.Name, &function{file: v.file, node: n, usedBy: []*ast.FuncDecl{}})
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
	case *ast.Ident:
		pkg := v.file.Pkg
		o, ok := pkg.TypesInfo.Uses[n]
		if !ok {
			return v
		}

		typeName := o.Type().Underlying().String()

		if strings.HasPrefix(typeName, "func(") {
			f := v.r.getFunction(pkg, n.Name)
			f.addUser(nil)
		}
	}

	return v
}

type functionBodyWalker struct {
	pkg *lint.Package
	f   *ast.FuncDecl
	r   *UnusedFunctionRule
}

func (w functionBodyWalker) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.CallExpr:
		id, ok := n.Fun.(*ast.Ident)
		if !ok {
			return w // ignore calls of type pkgname.funcName
		}

		fc := w.r.getFunction(w.pkg, id.Name)
		fc.addUser(w.f)
	case *ast.Ident:
		tv, ok := w.pkg.TypesInfo.Types[n]
		if !ok {
			return w
		}
		typeName := tv.Type.Underlying().String()

		if strings.HasPrefix(typeName, "func(") {
			f := w.r.getFunction(w.pkg, n.Name)
			f.addUser(w.f)
		}
	}

	return w
}
