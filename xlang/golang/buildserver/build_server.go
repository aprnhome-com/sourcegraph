package buildserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"

	"github.com/sourcegraph/ctxvfs"
	"github.com/sourcegraph/go-langserver/langserver"
	"github.com/sourcegraph/go-langserver/pkg/lsp"
	lsext "github.com/sourcegraph/go-langserver/pkg/lspext"
	"github.com/sourcegraph/jsonrpc2"
	"sourcegraph.com/sourcegraph/sourcegraph/xlang/lspext"
	"sourcegraph.com/sourcegraph/sourcegraph/xlang/vfsutil"
)

// NewHandler creates a new build server wrapping a (also newly
// created) Go language server. I.e., it creates a BuildHandler
// wrapping a LangHandler. The two handlers share a file system (in
// memory).
//
// The build server is responsible for things such as fetching
// dependencies, setting up the right file system structure and paths,
// and mapping local file system paths to logical URIs (e.g.,
// /goroot/src/fmt/print.go ->
// git://github.com/golang/go?go1.7.1#src/fmt/print.go).
func NewHandler() jsonrpc2.Handler {
	shared := &langserver.HandlerShared{Shared: true}
	h := &BuildHandler{
		HandlerShared: shared,
		lang:          &langserver.LangHandler{HandlerShared: shared},
	}
	// We want the langservers typechecker to use the buildservers package
	// finder.
	shared.FindPackage = h.findPackage
	return jsonrpc2.HandlerWithError(h.handle)
}

// BuildHandler is a Go build server LSP/JSON-RPC handler that wraps a
// Go language server handler.
type BuildHandler struct {
	lang *langserver.LangHandler

	mu                    sync.Mutex
	fetchAndSendDepsOnces map[string]*sync.Once // key is file URI
	depURLMus             map[string]*sync.Mutex
	gopathDeps            []*directory
	pinnedDepsOnce        sync.Once
	pinnedDeps            pinnedPkgs
	langserver.HandlerCommon
	*langserver.HandlerShared
	init           *lspext.InitializeParams // set by "initialize" request
	rootImportPath string                   // root import path of the workspace (e.g., "github.com/foo/bar")
}

func (h *BuildHandler) fetchAndSendDepsOnce(fileURI string) *sync.Once {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fetchAndSendDepsOnces == nil {
		h.fetchAndSendDepsOnces = map[string]*sync.Once{}
	}
	once, ok := h.fetchAndSendDepsOnces[fileURI]
	if !ok {
		once = new(sync.Once)
		h.fetchAndSendDepsOnces[fileURI] = once
	}
	return once
}

// Used to prevent concurrent fetches of a dependency
func (h *BuildHandler) depURLMu(path string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.depURLMus == nil {
		h.depURLMus = make(map[string]*sync.Mutex)
	}
	mu, ok := h.depURLMus[path]
	if !ok {
		mu = new(sync.Mutex)
		h.depURLMus[path] = mu
	}
	return mu
}

const (
	gopath     = "/"
	goroot     = "/goroot"
	gocompiler = "gc"

	// TODO(sqs): allow these to be customized. They're
	// fine for now, though.
	goos   = "linux"
	goarch = "amd64"
)

// RuntimeVersion is the version of go stdlib to use. We allow it to be
// different to runtime.Version for test data.
var RuntimeVersion = runtime.Version()

// reset clears all internal state in h.
func (h *BuildHandler) reset(init *lspext.InitializeParams, rootURI string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.HandlerCommon.Reset(rootURI); err != nil {
		return err
	}
	if err := h.HandlerShared.Reset(rootURI, false); err != nil {
		return err
	}
	h.init = init
	h.fetchAndSendDepsOnces = nil
	h.depURLMus = nil
	h.gopathDeps = nil
	h.pinnedDepsOnce = sync.Once{}
	h.pinnedDeps = nil
	return nil
}

func (h *BuildHandler) handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (result interface{}, err error) {
	// Prevent any uncaught panics from taking the entire server down.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("unexpected panic: %v", r)

			// Same as net/http
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			log.Printf("panic serving %v: %v\n%s", req.Method, r, buf)
			return
		}
	}()

	h.mu.Lock()
	if req.Method != "initialize" && h.init == nil {
		h.mu.Unlock()
		return nil, errors.New("server must be initialized")
	}
	h.mu.Unlock()
	if err := h.CheckReady(); err != nil {
		if req.Method == "exit" {
			err = nil
		}
		return nil, err
	}

	h.InitTracer(conn)
	span, ctx, err := h.SpanForRequest(ctx, "build", req, opentracing.Tags{"mode": "go"})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			ext.Error.Set(span, true)
			span.LogEvent(fmt.Sprintf("error: %v", err))
		}
		span.Finish()
	}()

	switch {
	case req.Method == "initialize":
		if h.init != nil {
			return nil, errors.New("build server is already initialized")
		}
		if req.Params == nil {
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams}
		}
		var params lspext.InitializeParams
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			return nil, err
		}

		// Determine the root import path of this workspace (e.g., "github.com/user/repo").
		span.SetTag("originalRootPath", params.OriginalRootPath)
		fs := vfsutil.RemoteFS(conn)
		rootImportPath, err := h.determineRootImportPath(ctx, params.OriginalRootPath, fs)
		if err != nil {
			return nil, fmt.Errorf("unable to determine workspace's root Go import path: %s (original rootPath is %q)", err, params.OriginalRootPath)
		}
		// Sanity-check the import path.
		if rootImportPath == "" || rootImportPath != path.Clean(rootImportPath) || strings.Contains(rootImportPath, "..") || strings.HasPrefix(rootImportPath, string(os.PathSeparator)) || strings.HasPrefix(rootImportPath, "/") || strings.HasPrefix(rootImportPath, ".") {
			return nil, fmt.Errorf("empty or suspicious import path: %q", rootImportPath)
		}
		var isStdlib bool
		if rootImportPath == "github.com/golang/go" {
			rootImportPath = ""
			isStdlib = true
		} else {
			h.rootImportPath = rootImportPath
		}

		// Send "initialize" to the wrapped lang server.
		langInitParams := langserver.InitializeParams{
			InitializeParams:     params.InitializeParams,
			NoOSFileSystemAccess: true,
			BuildContext: &langserver.InitializeBuildContextParams{
				GOOS:       goos,
				GOARCH:     goarch,
				GOPATH:     gopath,
				GOROOT:     goroot,
				CgoEnabled: false,
				Compiler:   gocompiler,

				// TODO(sqs): We'd like to set this to true only for
				// the package we're analyzing (or for the whole
				// repo), but go/loader is insufficiently
				// configurable, so it applies it to the entire
				// program, which takes a lot longer and causes weird
				// error messages in the runtime package, etc. Disable
				// it for now.
				UseAllFiles: false,
			},
		}

		// Put all files in the workspace under a /src/IMPORTPATH
		// directory, such as /src/github.com/foo/bar, so that Go can
		// build it in GOPATH=/.
		if isStdlib {
			langInitParams.RootPath = "file://" + goroot
		} else {
			langInitParams.RootPath = "file://" + "/src/" + h.rootImportPath
		}
		langInitParams.RootImportPath = h.rootImportPath
		if err := h.reset(&params, langInitParams.RootPath); err != nil {
			return nil, err
		}
		h.FS.Bind(h.OverlayMountPath, fs, "/", ctxvfs.BindBefore)
		var langInitResp lsp.InitializeResult
		if err := h.callLangServer(ctx, conn, req.Method, req.Notif, langInitParams, &langInitResp); err != nil {
			return nil, err
		}
		return langInitResp, nil

	case req.Method == "shutdown":
		h.ShutDown()
		return nil, nil

	case req.Method == "exit":
		conn.Close()
		return nil, nil

	case req.Method == "workspace/xdependencies":
		// The same as h.fetchAndSendDepsOnce except it operates locally to the
		// request.
		fetchAndSendDepsOnces := make(map[string]*sync.Once) // key is file URI
		localFetchAndSendDepsOnce := func(fileURI string) *sync.Once {
			once, ok := fetchAndSendDepsOnces[fileURI]
			if !ok {
				once = new(sync.Once)
				fetchAndSendDepsOnces[fileURI] = once
			}
			return once
		}

		var (
			mu         sync.Mutex
			references []lspext.DependencyReference
		)
		emitRef := func(path string, r goDependencyReference) {
			// If the _reference_ to a definition is made from inside a
			// vendored package, or from outside of the repository itself,
			// exclude it.
			if langserver.IsVendorDir(path) || !langserver.PathHasPrefix(path, h.RootFSPath) {
				return
			}

			// If the package being referenced is defined in the repo, and
			// it is NOT a vendor package, then exclude it.
			if !r.vendor && langserver.PathHasPrefix(filepath.Join(gopath, "src", r.absolute), h.RootFSPath) {
				return
			}

			newURI, err := h.rewriteURIFromLangServer("file://" + path)
			if err != nil {
				log.Printf("error rewriting URI from language server: %s", err)
				return
			}

			mu.Lock()
			references = append(references, lspext.DependencyReference{
				Attributes: r.attributes(),
				Hints: map[string]interface{}{
					"dir": newURI,
				},
			})
			mu.Unlock()
		}

		// We need every transitive dependency, for every Go package in the
		// repository.
		var (
			w  = ctxvfs.Walk(ctx, h.RootFSPath, h.FS)
			dc = newDepCache()
		)
		dc.collectReferences = true
		for w.Step() {
			if path.Ext(w.Path()) == ".go" {
				d := path.Dir(w.Path())
				localFetchAndSendDepsOnce(d).Do(func() {
					if err := h.fetchTransitiveDepsOfFile(ctx, d, dc); err != nil {
						log.Printf("Warning: fetching deps for dir %s: %s.", d, err)
					}
				})
			}
		}
		dc.references(emitRef, 1)
		return references, nil

	default:
		// Pass the request onto the lang server.

		// Rewrite URI fields in params to refer to file paths inside
		// the GOPATH at the appropriate import path directory. E.g.:
		//
		//   file:///dir/file.go -> file:///src/github.com/user/repo/dir/file.go
		var urisInRequest []string // rewritten
		var params interface{}
		if req.Params != nil {
			if err := json.Unmarshal(*req.Params, &params); err != nil {
				return nil, err
			}
		}
		rewriteURIFromClient := func(uri string) string {
			if !strings.HasPrefix(uri, "file:///") {
				panic("URI in LSP request must be a file:/// URI, got " + uri)
			}
			path := strings.TrimPrefix(uri, "file://")
			path = pathpkg.Join(h.RootFSPath, path)
			if !langserver.PathHasPrefix(path, h.RootFSPath) {
				panic(fmt.Sprintf("file path %q must have prefix %q (file URI is %q, root URI is %q)", path, h.RootFSPath, uri, h.init.RootPath))
			}
			newURI := "file://" + path
			urisInRequest = append(urisInRequest, newURI) // collect
			return newURI
		}
		lspext.WalkURIFields(params, nil, rewriteURIFromClient)
		// Store back to req.Params to avoid 2 different versions of the data.
		if req.Params != nil {
			b, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			req.Params = (*json.RawMessage)(&b)
		}

		// workspace/symbol queries must have their `dir:` query filter
		// rewritten for github.com/golang/go due to its specialized directory
		// structure. e.g. `dir:src/net/http` should work, but the LS will
		// expect `dir:net/http` as any real/valid Go project will have package
		// paths align with the directory structure.
		if req.Method == "workspace/symbol" && strings.HasPrefix(h.init.OriginalRootPath, "git://github.com/golang/go") {
			var wsparams lsp.WorkspaceSymbolParams
			if err := json.Unmarshal(*req.Params, &wsparams); err != nil {
				return nil, err
			}
			q := langserver.ParseQuery(wsparams.Query)
			if q.Filter == langserver.FilterDir {
				// If the query does not start with `src/` and it is a request
				// for a stdlib dir, it should return no results (the filter is
				// dir, not package path).
				if _, isStdlib := stdlibPackagePaths[q.Dir]; isStdlib && !strings.HasPrefix(q.Dir, "src") {
					q.Dir = "sginvalid"
				} else {
					q.Dir = langserver.PathTrimPrefix(q.Dir, "src") // "src/net/http" -> "net/http"
				}
			}
			wsparams.Query = q.String()
			b, err := json.Marshal(wsparams)
			if err != nil {
				return nil, err
			}
			req.Params = (*json.RawMessage)(&b)
		}

		// Immediately handle file system requests by adding them to
		// the VFS shared between the build and lang server.
		if langserver.IsFileSystemRequest(req.Method) {
			if err := h.HandleFileSystemRequest(ctx, req); err != nil {
				return nil, err
			}
			return nil, nil
		}

		// All our textDocument methods fetch dependencies just in
		// time, except documentSymbol.
		shouldFetchDeps := true
		if strings.HasPrefix(req.Method, "textDocument/") && req.Method != "textDocument/documentSymbol" {
			shouldFetchDeps = false
		}

		// Fetch transitive dependencies for the named files, if this
		// is a language analysis request.
		if shouldFetchDeps {
			for _, uri := range urisInRequest {
				h.fetchAndSendDepsOnce(uri).Do(func() {
					if err := h.fetchTransitiveDepsOfFile(ctx, uri, newDepCache()); err != nil {
						log.Printf("Warning: fetching deps for Go file %q: %s.", uri, err)
					}
				})
			}
		}
		if req.Method == "workspace/xreferences" {
			// Parse the parameters and if a dir hint is present, rewrite the
			// URI.
			var p lsext.WorkspaceReferencesParams
			if err := json.Unmarshal(*req.Params, &p); err != nil {
				return nil, err
			}
			dirHint, haveDirHint := p.Hints["dir"]
			if haveDirHint {
				dirHint = rewriteURIFromClient(dirHint.(string))
				p.Hints["dir"] = dirHint
				b, err := json.Marshal(p)
				if err != nil {
					return nil, err
				}
				req.Params = (*json.RawMessage)(&b)
			}

			// Fetch transitive dependencies for either a specific directory
			// (the dir hint) OR every Go package in the repository.
			w := ctxvfs.Walk(ctx, h.RootFSPath, h.FS)
			for w.Step() {
				d := path.Dir(w.Path())
				if haveDirHint && "file://"+d != dirHint.(string) {
					continue
				}
				if !haveDirHint && path.Ext(w.Path()) != ".go" {
					continue
				}
				h.fetchAndSendDepsOnce(d).Do(func() {
					if err := h.fetchTransitiveDepsOfFile(ctx, d, newDepCache()); err != nil {
						log.Printf("Warning: fetching deps for dir %s: %s.", d, err)
					}
				})
			}
		}

		var result interface{}
		if err := h.callLangServer(ctx, conn, req.Method, req.Notif, req.Params, &result); err != nil {
			return nil, err
		}

		// (Un-)rewrite URI fields in the result. E.g.:
		//
		//   file:///src/github.com/user/repo/dir/file.go -> file:///dir/file.go
		var walkErr error
		lspext.WalkURIFields(result, nil, func(uri string) string {
			newURI, err := h.rewriteURIFromLangServer(uri)
			if err != nil {
				walkErr = err
			}
			return newURI
		})
		if walkErr != nil {
			return nil, fmt.Errorf("%s (in Go language server response)", walkErr)
		}
		return result, nil
	}
}

func (h *BuildHandler) rewriteURIFromLangServer(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if !u.IsAbs() {
		return "", fmt.Errorf("invalid relative URI %q", u)
	}

	switch u.Scheme {
	case "file":
		if !filepath.IsAbs(u.Path) {
			return "", fmt.Errorf("invalid relative file path in URI %q", uri)
		}

		// Refers to a file in the Go stdlib?
		if langserver.PathHasPrefix(u.Path, goroot) {
			fileInGoStdlib := langserver.PathTrimPrefix(u.Path, goroot)
			if h.rootImportPath == "" {
				// The workspace is the Go stdlib and this refers to
				// something in the Go stdlib, so let's use file:///
				// so that the LSP proxy adds our current rev, instead
				// of using runtime.Version() (which is not
				// necessarily the commit of the Go stdlib we're
				// analyzing).
				return "file:///" + fileInGoStdlib, nil
			}
			return "git://github.com/golang/go?" + RuntimeVersion + "#" + fileInGoStdlib, nil
		}

		// Refers to a file in the same workspace?
		if langserver.PathHasPrefix(u.Path, h.RootFSPath) {
			pathInThisWorkspace := langserver.PathTrimPrefix(u.Path, h.RootFSPath)
			return "file:///" + pathInThisWorkspace, nil
		}

		// Refers to a file in the GOPATH (that's from another repo)?
		if gopathSrcDir := path.Join(gopath, "src"); langserver.PathHasPrefix(u.Path, gopathSrcDir) {
			p := langserver.PathTrimPrefix(u.Path, gopathSrcDir) // "github.com/foo/bar/baz/qux.go"

			// Go through the list of directories we have
			// mounted. We make a copy instead of holding the lock
			// in the for loop to avoid holding the lock for
			// longer than necessary.
			h.HandlerShared.Mu.Lock()
			deps := make([]*directory, len(h.gopathDeps))
			copy(deps, h.gopathDeps)
			h.HandlerShared.Mu.Unlock()
			var d *directory
			for _, dep := range deps {
				if strings.HasPrefix(p, dep.projectRoot) {
					d = dep
				}
			}
			if d != nil {
				rev := d.rev
				if rev == "" {
					rev = "HEAD"
				}

				i := strings.Index(d.cloneURL, "://")
				if i >= 0 {
					repo := strings.TrimSuffix(d.cloneURL[i+len("://"):], "."+d.vcs)
					path := strings.TrimPrefix(strings.TrimPrefix(p, d.projectRoot), "/")
					return fmt.Sprintf("%s://%s?%s#%s", d.vcs, repo, rev, path), nil
				}
			}
		}

		return "unresolved:" + u.Path, nil
	default:
		return "", fmt.Errorf("invalid non-file URI %q", uri)
	}
}

// callLangServer sends the (usually modified) request to the wrapped
// Go language server. It
//
// Although bypasses the JSON-RPC wire protocol ( just sending it
// in-memory for simplicity/speed), it behaves in the same way as
// though the peer language server were remote. The conn is nil (and
// the request ID is zero'd out) to prevent the language server from
// breaking this abstraction.
func (h *BuildHandler) callLangServer(ctx context.Context, conn *jsonrpc2.Conn, method string, notif bool, params, result interface{}) error {
	req := jsonrpc2.Request{
		Method: method,
		Notif:  notif,
	}
	if err := req.SetParams(params); err != nil {
		return err
	}

	wrappedConn := &jsonrpc2ConnImpl{rewriteURI: h.rewriteURIFromLangServer, conn: conn}

	result0, err := h.lang.Handle(ctx, wrappedConn, &req)
	if err != nil {
		return err
	}

	if !notif {
		// Don't pass the interface{} value, to avoid the build and
		// language servers from breaking the abstraction that they are in
		// separate memory spaces.
		b, err := json.Marshal(result0)
		if err != nil {
			return err
		}
		if result != nil {
			if err := json.Unmarshal(b, result); err != nil {
				return err
			}
		}
	}
	return nil
}
