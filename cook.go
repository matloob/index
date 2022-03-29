package index

import (
	"fmt"
	"go/build"
	"go/token"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
)

// TODO(matloob): take build.Context instead... for later...?

// Assumption: directory is in module cache.

func Cook(ctxt *build.Context, rp *RawPackage, mode ImportMode) (*build.Package, error) {
	p := &build.Package{}

	const path = "." // TODO(matloob): clean this up; ImportDir calls ctxt.Import with path == "."
	srcDir := rp.SrcDir

	var pkgtargetroot string
	var pkga string
	var pkgerr error
	suffix := ""
	if ctxt.InstallSuffix != "" {
		suffix = "_" + ctxt.InstallSuffix
	}
	switch ctxt.Compiler {
	case "gccgo":
		pkgtargetroot = "pkg/gccgo_" + ctxt.GOOS + "_" + ctxt.GOARCH + suffix
	case "gc":
		pkgtargetroot = "pkg/" + ctxt.GOOS + "_" + ctxt.GOARCH + suffix
	default:
		// Save error for end of function.
		pkgerr = fmt.Errorf("import %q: unknown compiler %q", path, ctxt.Compiler)
	}
	setPkga := func() {
		switch ctxt.Compiler {
		case "gccgo":
			dir, elem := pathpkg.Split(p.ImportPath)
			pkga = pkgtargetroot + "/" + dir + "lib" + elem + ".a"
		case "gc":
			pkga = pkgtargetroot + "/" + p.ImportPath + ".a"
		}
	}
	setPkga()

	binaryOnly := false
	pkga = "" // local imports have no installed path
	if srcDir == "" {
		return p, fmt.Errorf("import %q: import relative to unknown directory", path)
	}
	if !isAbsPath(path) {
		p.Dir = joinPath(srcDir, path)
	}
	// p.Dir directory may or may not exist. Gather partial information first, check if it exists later.
	// Determine canonical import path, if any.
	// Exclude results where the import path would include /testdata/.

	// Assumption: directory is in the module cache.

	// It's okay that we didn't find a root containing dir.
	// Keep going with the information we have.

	if p.Root != "" {
		p.SrcRoot = joinPath(p.Root, "src")
		p.PkgRoot = joinPath(p.Root, "pkg")
		p.BinDir = joinPath(p.Root, "bin")
		if pkga != "" {
			p.PkgTargetRoot = joinPath(p.Root, pkgtargetroot)
			p.PkgObj = joinPath(p.Root, pkga)
		}
	}

	if mode&FindOnly != 0 {
		return p, pkgerr
	}
	// TODO(matloob): remove this? impossible for binaryOnly to be set here...
	if binaryOnly && (mode&AllowBinary) != 0 {
		return p, pkgerr
	}

	// We need to do a second round of bad file processing.
	var badGoError error
	badFiles := make(map[string]bool)
	badFile := func(tf TaggedFile, err error) {
		if badGoError == nil {
			badGoError = err
		}
		if !badFiles[tf.Name] {
			p.InvalidGoFiles = append(p.InvalidGoFiles, tf.Name)
			badFiles[tf.Name] = true
		}
	}

	var firstCommentFile string
	embedPos := make(map[string][]token.Position)
	testEmbedPos := make(map[string][]token.Position)
	xTestEmbedPos := make(map[string][]token.Position)
	importPos := make(map[string][]token.Position)
	testImportPos := make(map[string][]token.Position)
	xTestImportPos := make(map[string][]token.Position)
	for _, tf := range rp.GoFiles {
		if tf.IgnoreFile {
			if strings.HasPrefix(tf.Name, "_") || strings.HasPrefix(tf.Name, ".") {
				// not due to build constraints - don't report
			} else {
				p.IgnoredGoFiles = append(p.IgnoredGoFiles, tf.Name)
			}
			continue
		}

		// *****
		//
		//
		//
		//
		//
		//Match pattern here.

		if mode&ImportComment != 0 {
			com, err := strconv.Unquote(tf.QuotedImportComment)
			if err != nil {
				badFile(tf, fmt.Errorf("%s:%d: cannot parse import comment", tf.Name, tf.QuotedImportCommentLine))
			} else if p.ImportComment == "" {
				p.ImportComment = com
				firstCommentFile = tf.Name
			} else if p.ImportComment != com {
				badFile(tf, fmt.Errorf("found import comments %q (%s) and %q (%s) in %s", p.ImportComment, firstCommentFile, com, tf, p.Dir))
			}
		}

		// TODO(matloob): determine pkg name here? pkg variable

		isTest := strings.HasSuffix(tf.Name, "_test.go")
		isXTest := false
		if isTest && strings.HasSuffix(tf.PkgName, "_test") && p.Name != tf.PkgName {
			isXTest = true
		}

		// Record imports and information about cgo.
		isCgo := false
		for _, imp := range tf.Imports {
			if imp.Path == "C" {
				if isTest {
					badFile(tf, fmt.Errorf("use of cgo in test %s not supported", tf.Name))
					continue
				}
				isCgo = true

				if imp.Doc != "" {
					if err := saveCgo(ctxt, tf.Name, p, imp.Doc); err != nil {
						badFile(tf, err)
					}
				}

			}
		}

		var fileList *[]string
		var importMap, embedMap map[string][]token.Position
		switch {
		case isCgo:
			//allTags["cgo"] = true
			if ctxt.CgoEnabled {
				fileList = &p.CgoFiles
				importMap = importPos
				embedMap = embedPos
			} else {
				// Ignore imports and embeds from cgo files if cgo is disabled.
				fileList = &p.IgnoredGoFiles
			}
		case isXTest:
			fileList = &p.XTestGoFiles
			importMap = xTestImportPos
			embedMap = xTestEmbedPos
		case isTest:
			fileList = &p.TestGoFiles
			importMap = testImportPos
			embedMap = testEmbedPos
		default:
			fileList = &p.GoFiles
			importMap = importPos
			embedMap = embedPos
		}
		*fileList = append(*fileList, tf.Name)
		if importMap != nil {
			for _, imp := range tf.Imports {
				importMap[imp.Path] = append(importMap[imp.Path], imp.Position)
			}
		}
		if embedMap != nil {
			for k, v := range tf.Embeds {
				embedMap[k] = v
			}
		}
	}

	/// ******** Examine other file types here.....

	if ctxt.Compiler == "gccgo" && p.Goroot {
		// gccgo has no sources for GOROOT packages.
		return p, nil
	}

	return p, nil
}

// joinPath calls joinPath (if not nil) or else filepath.Join.
func joinPath(elem ...string) string {
	return filepath.Join(elem...)
}

// splitPathList calls ctxt.SplitPathList (if not nil) or else filepath.SplitList.
func splitPathList(s string) []string {
	return filepath.SplitList(s)
}

// isAbsPath calls isAbsPath (if not nil) or else filepath.IsAbs.
func isAbsPath(path string) bool {
	return filepath.IsAbs(path)
}

// hasSubdir calls ctxt.HasSubdir (if not nil) or else uses
// the local file system to answer the question.
func ctxthasSubdir(root, dir string) (rel string, ok bool) {

	// Try using paths we received.
	if rel, ok = hasSubdir(root, dir); ok {
		return
	}

	// Try expanding symlinks and comparing
	// expanded against unexpanded and
	// expanded against expanded.
	rootSym, _ := filepath.EvalSymlinks(root)
	dirSym, _ := filepath.EvalSymlinks(dir)

	if rel, ok = hasSubdir(rootSym, dir); ok {
		return
	}
	if rel, ok = hasSubdir(root, dirSym); ok {
		return
	}
	return hasSubdir(rootSym, dirSym)
}

// readDir calls ctxt.ReadDir (if not nil) or else ioutil.ReadDir.
func readDir(path string) ([]fs.FileInfo, error) {
	return ioutil.ReadDir(path)
}

// openFile calls ctxt.OpenFile (if not nil) or else os.Open.
func openFile(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err // nil interface
	}
	return f, nil
}

// isFile determines whether path is a file by trying to open it.
// It reuses openFile instead of adding another function to the
// list in Context.
func isFile(path string) bool {
	f, err := openFile(path)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func saveCgo(ctxt *build.Context, filename string, di *build.Package, importComment string) error {
	text := importComment
	for _, line := range strings.Split(text, "\n") {
		orig := line

		// Line is
		//	#cgo [GOOS/GOARCH...] LDFLAGS: stuff
		//
		line = strings.TrimSpace(line)
		if len(line) < 5 || line[:4] != "#cgo" || (line[4] != ' ' && line[4] != '\t') {
			continue
		}

		// Split at colon.
		line, argstr, ok := strings.Cut(strings.TrimSpace(line[4:]), ":")
		if !ok {
			return fmt.Errorf("%s: invalid #cgo line: %s", filename, orig)
		}

		// Parse GOOS/GOARCH stuff.
		f := strings.Fields(line)
		if len(f) < 1 {
			return fmt.Errorf("%s: invalid #cgo line: %s", filename, orig)
		}

		cond, verb := f[:len(f)-1], f[len(f)-1]
		if len(cond) > 0 {
			ok := false
			for _, c := range cond {
				if ctxt.matchAuto(c, nil) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}

		args, err := splitQuoted(argstr)
		if err != nil {
			return fmt.Errorf("%s: invalid #cgo line: %s", filename, orig)
		}
		for i, arg := range args {
			if arg, ok = expandSrcDir(arg, di.Dir); !ok {
				return fmt.Errorf("%s: malformed #cgo argument: %s", filename, arg)
			}
			args[i] = arg
		}

		switch verb {
		case "CFLAGS", "CPPFLAGS", "CXXFLAGS", "FFLAGS", "LDFLAGS":
			// Change relative paths to absolute.
			ctxt.makePathsAbsolute(args, di.Dir)
		}

		switch verb {
		case "CFLAGS":
			di.CgoCFLAGS = append(di.CgoCFLAGS, args...)
		case "CPPFLAGS":
			di.CgoCPPFLAGS = append(di.CgoCPPFLAGS, args...)
		case "CXXFLAGS":
			di.CgoCXXFLAGS = append(di.CgoCXXFLAGS, args...)
		case "FFLAGS":
			di.CgoFFLAGS = append(di.CgoFFLAGS, args...)
		case "LDFLAGS":
			di.CgoLDFLAGS = append(di.CgoLDFLAGS, args...)
		case "pkg-config":
			di.CgoPkgConfig = append(di.CgoPkgConfig, args...)
		default:
			return fmt.Errorf("%s: invalid #cgo verb: %s", filename, orig)
		}
	}
	return nil
}
