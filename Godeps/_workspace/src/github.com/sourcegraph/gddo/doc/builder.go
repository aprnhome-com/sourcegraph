// Copyright 2013 The Go Authors. All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd.

package doc

import (
	"bytes"
	"errors"
	"go/ast"
	"go/build"
	"go/doc"
	"go/format"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sourcegraph/gddo/gosrc"
)

func startsWithUppercase(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

var badSynopsisPrefixes = []string{
	"Autogenerated by Thrift Compiler",
	"Automatically generated ",
	"Auto-generated by ",
	"Copyright ",
	"COPYRIGHT ",
	`THE SOFTWARE IS PROVIDED "AS IS"`,
	"TODO: ",
	"vim:",
}

// synopsis extracts the first sentence from s. All runs of whitespace are
// replaced by a single space.
func synopsis(s string) string {

	parts := strings.SplitN(s, "\n\n", 2)
	s = parts[0]

	var buf []byte
	const (
		other = iota
		period
		space
	)
	last := space
Loop:
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch b {
		case ' ', '\t', '\r', '\n':
			switch last {
			case period:
				break Loop
			case other:
				buf = append(buf, ' ')
				last = space
			}
		case '.':
			last = period
			buf = append(buf, b)
		default:
			last = other
			buf = append(buf, b)
		}
	}

	// Ensure that synopsis fits an App Engine datastore text property.
	const m = 400
	if len(buf) > m {
		buf = buf[:m]
		if i := bytes.LastIndex(buf, []byte{' '}); i >= 0 {
			buf = buf[:i]
		}
		buf = append(buf, " ..."...)
	}

	s = string(buf)

	r, n := utf8.DecodeRuneInString(s)
	if n < 0 || unicode.IsPunct(r) || unicode.IsSymbol(r) {
		// ignore Markdown headings, editor settings, Go build constraints, and * in poorly formatted block comments.
		s = ""
	} else {
		for _, prefix := range badSynopsisPrefixes {
			if strings.HasPrefix(s, prefix) {
				s = ""
				break
			}
		}
	}

	return s
}

var referencesPats = []*regexp.Regexp{
	regexp.MustCompile(`"([-a-zA-Z0-9~+_./]+)"`), // quoted path
	regexp.MustCompile(`https://drone\.io/([-a-zA-Z0-9~+_./]+)/status\.png`),
	regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`go\s+get\s+`,
		`goinstall\s+`,
		regexp.QuoteMeta("http://godoc.org/"),
		regexp.QuoteMeta("http://gopkgdoc.appspot.com/pkg/"),
		regexp.QuoteMeta("http://go.pkgdoc.org/"),
		regexp.QuoteMeta("http://gowalker.org/"),
	}, "|") + `)([-a-zA-Z0-9~+_./]+)`),
}

// addReferences adds packages referenced in plain text s.
func addReferences(references map[string]bool, s []byte) {
	for _, pat := range referencesPats {
		for _, m := range pat.FindAllSubmatch(s, -1) {
			p := string(m[1])
			if gosrc.IsValidRemotePath(p) {
				references[p] = true
			}
		}
	}
}

type byFuncName []*doc.Func

func (s byFuncName) Len() int           { return len(s) }
func (s byFuncName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s byFuncName) Less(i, j int) bool { return s[i].Name < s[j].Name }

func removeAssociations(dpkg *doc.Package) {
	for _, t := range dpkg.Types {
		dpkg.Funcs = append(dpkg.Funcs, t.Funcs...)
		t.Funcs = nil
	}
	sort.Sort(byFuncName(dpkg.Funcs))
}

// builder holds the state used when building the documentation.
type builder struct {
	srcs     map[string]*source
	fset     *token.FileSet
	examples []*doc.Example
	buf      []byte // scratch space for printNode method.
}

type Value struct {
	Decl Code
	Pos  Pos
	Doc  string
}

func (b *builder) values(vdocs []*doc.Value) []*Value {
	var result []*Value
	for _, d := range vdocs {
		result = append(result, &Value{
			Decl: b.printDecl(d.Decl),
			Pos:  b.position(d.Decl),
			Doc:  d.Doc,
		})
	}
	return result
}

type Note struct {
	Pos  Pos
	UID  string
	Body string
}

type posNode token.Pos

func (p posNode) Pos() token.Pos { return token.Pos(p) }
func (p posNode) End() token.Pos { return token.Pos(p) }

func (b *builder) notes(gnotes map[string][]*doc.Note) map[string][]*Note {
	if len(gnotes) == 0 {
		return nil
	}
	notes := make(map[string][]*Note)
	for tag, gvalues := range gnotes {
		values := make([]*Note, len(gvalues))
		for i := range gvalues {
			values[i] = &Note{
				Pos:  b.position(posNode(gvalues[i].Pos)),
				UID:  gvalues[i].UID,
				Body: strings.TrimSpace(gvalues[i].Body),
			}
		}
		notes[tag] = values
	}
	return notes
}

type Example struct {
	Name   string
	Doc    string
	Code   Code
	Play   string
	Output string
}

var exampleOutputRx = regexp.MustCompile(`(?i)//[[:space:]]*output:`)

func (b *builder) getExamples(name string) []*Example {
	var docs []*Example
	for _, e := range b.examples {
		if !strings.HasPrefix(e.Name, name) {
			continue
		}
		n := e.Name[len(name):]
		if n != "" {
			if i := strings.LastIndex(n, "_"); i != 0 {
				continue
			}
			n = n[1:]
			if startsWithUppercase(n) {
				continue
			}
			n = strings.Title(n)
		}

		code, output := b.printExample(e)

		play := ""
		if e.Play != nil {
			b.buf = b.buf[:0]
			if err := format.Node(sliceWriter{&b.buf}, b.fset, e.Play); err != nil {
				play = err.Error()
			} else {
				play = string(b.buf)
			}
		}

		docs = append(docs, &Example{
			Name:   n,
			Doc:    e.Doc,
			Code:   code,
			Output: output,
			Play:   play})
	}
	return docs
}

type Func struct {
	Decl     Code
	Pos      Pos
	Doc      string
	Name     string
	Recv     string
	Examples []*Example
}

func (b *builder) funcs(fdocs []*doc.Func) []*Func {
	var result []*Func
	for _, d := range fdocs {
		var exampleName string
		switch {
		case d.Recv == "":
			exampleName = d.Name
		case d.Recv[0] == '*':
			exampleName = d.Recv[1:] + "_" + d.Name
		default:
			exampleName = d.Recv + "_" + d.Name
		}
		result = append(result, &Func{
			Decl:     b.printDecl(d.Decl),
			Pos:      b.position(d.Decl),
			Doc:      d.Doc,
			Name:     d.Name,
			Recv:     d.Recv,
			Examples: b.getExamples(exampleName),
		})
	}
	return result
}

type Type struct {
	Doc      string
	Name     string
	Decl     Code
	Pos      Pos
	Consts   []*Value
	Vars     []*Value
	Funcs    []*Func
	Methods  []*Func
	Examples []*Example
}

func (b *builder) types(tdocs []*doc.Type) []*Type {
	var result []*Type
	for _, d := range tdocs {
		result = append(result, &Type{
			Doc:      d.Doc,
			Name:     d.Name,
			Decl:     b.printDecl(d.Decl),
			Pos:      b.position(d.Decl),
			Consts:   b.values(d.Consts),
			Vars:     b.values(d.Vars),
			Funcs:    b.funcs(d.Funcs),
			Methods:  b.funcs(d.Methods),
			Examples: b.getExamples(d.Name),
		})
	}
	return result
}

var packageNamePats = []*regexp.Regexp{
	// Last element with .suffix removed.
	regexp.MustCompile(`/([^-./]+)[-.](?:git|svn|hg|bzr|v\d+)$`),

	// Last element with "go" prefix or suffix removed.
	regexp.MustCompile(`/([^-./]+)[-.]go$`),
	regexp.MustCompile(`/go[-.]([^-./]+)$`),

	// Special cases for popular repos.
	regexp.MustCompile(`^code\.google\.com/p/google-api-go-client/([^/]+)/v[^/]+$`),
	regexp.MustCompile(`^code\.google\.com/p/biogo\.([^/]+)$`),

	// It's also common for the last element of the path to contain an
	// extra "go" prefix, but not always. TODO: examine unresolved ids to
	// detect when trimming the "go" prefix is appropriate.

	// Last component of path.
	regexp.MustCompile(`([^/]+)$`),
}

func simpleImporter(imports map[string]*ast.Object, path string) (*ast.Object, error) {
	pkg := imports[path]
	if pkg != nil {
		return pkg, nil
	}

	// Guess the package name without importing it.
	for _, pat := range packageNamePats {
		m := pat.FindStringSubmatch(path)
		if m != nil {
			pkg = ast.NewObj(ast.Pkg, m[1])
			pkg.Data = ast.NewScope(nil)
			imports[path] = pkg
			return pkg, nil
		}
	}

	return nil, errors.New("package not found")
}

type File struct {
	Name string
	URL  string
}

type Pos struct {
	Line int32  // 0 if not valid.
	N    uint16 // number of lines - 1
	File int16  // index in Package.Files
}

type source struct {
	name      string
	browseURL string
	data      []byte
	index     int
}

// PackageVersion is modified when previously stored packages are invalid.
const PackageVersion = "6"

type Package struct {
	// The import path for this package.
	ImportPath string

	// Import path prefix for all packages in the project.
	ProjectRoot string

	// Name of the project.
	ProjectName string

	// Project home page.
	ProjectURL string

	// Errors found when fetching or parsing this package.
	Errors []string

	// Packages referenced in README files.
	References []string

	// Version control system: git, hg, bzr, ...
	VCS string

	// Version control: belongs to a dead end fork
	DeadEndFork bool

	// The time this object was created.
	Updated time.Time

	// Cache validation tag. This tag is not necessarily an HTTP entity tag.
	// The tag is "" if there is no meaningful cache validation for the VCS.
	Etag string

	// Subdirectories, possibly containing Go code.
	Subdirectories []string

	// Package name or "" if no package for this import path. The proceeding
	// fields are set even if a package is not found for the import path.
	Name string

	// Synopsis and full documentation for the package.
	Synopsis string
	Doc      string

	// Format this package as a command.
	IsCmd bool

	// True if package documentation is incomplete.
	Truncated bool

	// Environment
	GOOS, GOARCH string

	// Top-level declarations.
	Consts []*Value
	Funcs  []*Func
	Types  []*Type
	Vars   []*Value

	// Package examples
	Examples []*Example

	Notes map[string][]*Note

	// Source.
	LineFmt   string
	BrowseURL string
	Files     []*File
	TestFiles []*File

	// Source size in bytes.
	SourceSize     int
	TestSourceSize int

	// Imports
	Imports      []string
	TestImports  []string
	XTestImports []string
}

var goEnvs = []struct{ GOOS, GOARCH string }{
	{"linux", "amd64"},
	{"darwin", "amd64"},
	{"windows", "amd64"},
}

// SetDefaultGOOS sets given GOOS value as default one to use when building
// package documents.
func SetDefaultGOOS(goos string) {
	if goos == "" {
		return
	}
	var i int
	for ; i < len(goEnvs); i++ {
		if goEnvs[i].GOOS == goos {
			break
		}
	}
	switch i {
	case 0:
		return
	case len(goEnvs):
		env := goEnvs[0]
		env.GOOS = goos
		goEnvs = append(goEnvs, env)
	}
	goEnvs[0], goEnvs[i] = goEnvs[i], goEnvs[0]
}

func NewPackage(dir *gosrc.Directory) (*Package, error) {

	pkg := &Package{
		Updated:        time.Now().UTC(),
		LineFmt:        dir.LineFmt,
		ImportPath:     dir.ImportPath,
		ProjectRoot:    dir.ProjectRoot,
		ProjectName:    dir.ProjectName,
		ProjectURL:     dir.ProjectURL,
		BrowseURL:      dir.BrowseURL,
		Etag:           PackageVersion + "-" + dir.Etag,
		VCS:            dir.VCS,
		DeadEndFork:    dir.DeadEndFork,
		Subdirectories: dir.Subdirectories,
	}

	var b builder
	b.srcs = make(map[string]*source)
	references := make(map[string]bool)
	for _, file := range dir.Files {
		if strings.HasSuffix(file.Name, ".go") {
			gosrc.OverwriteLineComments(file.Data)
			b.srcs[file.Name] = &source{name: file.Name, browseURL: file.BrowseURL, data: file.Data}
		} else {
			addReferences(references, file.Data)
		}
	}

	for r := range references {
		pkg.References = append(pkg.References, r)
	}

	if len(b.srcs) == 0 {
		return pkg, nil
	}

	b.fset = token.NewFileSet()

	// Find the package and associated files.

	ctxt := build.Context{
		GOOS:        "linux",
		GOARCH:      "amd64",
		CgoEnabled:  true,
		ReleaseTags: build.Default.ReleaseTags,
		BuildTags:   build.Default.BuildTags,
		Compiler:    "gc",
	}

	var err error
	var bpkg *build.Package

	for _, env := range goEnvs {
		ctxt.GOOS = env.GOOS
		ctxt.GOARCH = env.GOARCH
		bpkg, err = dir.Import(&ctxt, build.ImportComment)
		if _, ok := err.(*build.NoGoError); !ok {
			break
		}
	}
	if err != nil {
		if _, ok := err.(*build.NoGoError); !ok {
			pkg.Errors = append(pkg.Errors, err.Error())
		}
		return pkg, nil
	}

	if bpkg.ImportComment != "" && bpkg.ImportComment != dir.ImportPath {
		return nil, gosrc.NotFoundError{
			Message:  "not at canonical import path",
			Redirect: bpkg.ImportComment,
		}
	}

	// Parse the Go files

	files := make(map[string]*ast.File)
	names := append(bpkg.GoFiles, bpkg.CgoFiles...)
	sort.Strings(names)
	pkg.Files = make([]*File, len(names))
	for i, name := range names {
		file, err := parser.ParseFile(b.fset, name, b.srcs[name].data, parser.ParseComments)
		if err != nil {
			pkg.Errors = append(pkg.Errors, err.Error())
		} else {
			files[name] = file
		}
		src := b.srcs[name]
		src.index = i
		pkg.Files[i] = &File{Name: name, URL: src.browseURL}
		pkg.SourceSize += len(src.data)
	}

	apkg, _ := ast.NewPackage(b.fset, files, simpleImporter, nil)

	// Find examples in the test files.

	names = append(bpkg.TestGoFiles, bpkg.XTestGoFiles...)
	sort.Strings(names)
	pkg.TestFiles = make([]*File, len(names))
	for i, name := range names {
		file, err := parser.ParseFile(b.fset, name, b.srcs[name].data, parser.ParseComments)
		if err != nil {
			pkg.Errors = append(pkg.Errors, err.Error())
		} else {
			b.examples = append(b.examples, doc.Examples(file)...)
		}
		pkg.TestFiles[i] = &File{Name: name, URL: b.srcs[name].browseURL}
		pkg.TestSourceSize += len(b.srcs[name].data)
	}

	b.vetPackage(pkg, apkg)

	mode := doc.Mode(0)
	if pkg.ImportPath == "builtin" {
		mode |= doc.AllDecls
	}

	dpkg := doc.New(apkg, pkg.ImportPath, mode)

	if pkg.ImportPath == "builtin" {
		removeAssociations(dpkg)
	}

	pkg.Name = dpkg.Name
	pkg.Doc = strings.TrimRight(dpkg.Doc, " \t\n\r")
	pkg.Synopsis = synopsis(pkg.Doc)

	pkg.Examples = b.getExamples("")
	pkg.IsCmd = bpkg.IsCommand()
	pkg.GOOS = ctxt.GOOS
	pkg.GOARCH = ctxt.GOARCH

	pkg.Consts = b.values(dpkg.Consts)
	pkg.Funcs = b.funcs(dpkg.Funcs)
	pkg.Types = b.types(dpkg.Types)
	pkg.Vars = b.values(dpkg.Vars)
	pkg.Notes = b.notes(dpkg.Notes)

	pkg.Imports = bpkg.Imports
	pkg.TestImports = bpkg.TestImports
	pkg.XTestImports = bpkg.XTestImports

	return pkg, nil
}
