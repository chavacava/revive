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

type pkg2Functions struct {
	sync.Mutex
	funcs map[*lint.Package]map[string]*function
}

func (p2f *pkg2Functions) addFunction(pkg *lint.Package, fName string, nf *function) {
	p2f.Lock()
	if _, ok := p2f.funcs[pkg]; !ok {
		p2f.funcs[pkg] = map[string]*function{}
	}
	pkgFuncs := p2f.funcs[pkg]
	if _, ok := pkgFuncs[fName]; !ok {
		pkgFuncs[fName] = &function{}
	}
	f := pkgFuncs[fName]
	f.node = nf.node
	f.file = nf.file
	f.usedBy = append(f.usedBy, nf.usedBy...)
	p2f.Unlock()
}

func (p2f *pkg2Functions) getFunction(pkg *lint.Package, fName string) *function {
	p2f.Lock()
	pkgFuncs, ok := p2f.funcs[pkg]

	if ok {
		_, ok = pkgFuncs[fName]
	}
	p2f.Unlock()

	if !ok {
		p2f.addFunction(pkg, fName, &function{})
	}

	p2f.Lock()
	defer p2f.Unlock()
	return p2f.funcs[pkg][fName]

}

func (p2f *pkg2Functions) addUseToFunction(pkg *lint.Package, fName string, usedBy *ast.FuncDecl) {
	f := p2f.getFunction(pkg, fName)
	f.addUser(usedBy)
}

// map[pkg]->map[functionName]function
var functions = pkg2Functions{funcs: map[*lint.Package]map[string]*function{}}

// UnusedFunctionRule checks for unused (private) functions and methods
type UnusedFunctionRule struct{}

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

	pkgFuncs, ok := functions.funcs[p] // retrieve functions of package p
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
			functions.addFunction(v.file.Pkg, n.Name.Name, &function{file: v.file, node: n, usedBy: []*ast.FuncDecl{}})
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
			f := functions.getFunction(pkg, n.Name)
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

		fc := functions.getFunction(w.pkg, id.Name)
		fc.addUser(w.f)
	case *ast.Ident:
		tv, ok := w.pkg.TypesInfo.Types[n]
		if !ok {
			return w
		}
		typeName := tv.Type.Underlying().String()

		if strings.HasPrefix(typeName, "func(") {
			f := functions.getFunction(w.pkg, n.Name)
			f.addUser(w.f)
		}
	}

	return w
}
