package index

import (
	"errors"
	"fmt"
	"go/build"
	"go/build/constraint"
	"go/token"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TODO(matloob): take build.Context instead... for later...?

// Assumption: directory is in module cache.

func Cook(ctxt build.Context, rp *RawPackage, mode ImportMode) (*build.Package, error) {
	p := &build.Package{
		ImportPath: rp.Path,
		Dir:        rp.SrcDir,
	}
	if rp.Error != "" {
		return p, errors.New(rp.Error)
	}

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
	badFile := func(name string, err error) {
		if badGoError == nil {
			badGoError = err
		}
		if !badFiles[name] {
			p.InvalidGoFiles = append(p.InvalidGoFiles, name)
			badFiles[name] = true
		}
	}

	var Sfiles []string // files with ".S"(capital S)/.sx(capital s equivalent for case insensitive filesystems)
	var firstFile, firstCommentFile string
	embedPos := make(map[string][]token.Position)
	testEmbedPos := make(map[string][]token.Position)
	xTestEmbedPos := make(map[string][]token.Position)
	importPos := make(map[string][]token.Position)
	testImportPos := make(map[string][]token.Position)
	xTestImportPos := make(map[string][]token.Position)
	allTags := make(map[string]bool)
	for _, tf := range rp.SourceFiles {
		if tf.Error != "" {
			badFile(tf.Name, errors.New(tf.Error))
			continue
		} else if tf.ParseError != "" {
			badFile(tf.Name, errors.New(tf.ParseError))
			// Fall through: we might still have a partial AST in info.parsed,
			// and we want to list files with parse errors anyway.
		}

		var shouldBuild = true
		if !goodOSArchFile(ctxt, tf.Name, allTags) && !ctxt.UseAllFiles {
			shouldBuild = false
		} else if tf.GoBuildConstraint != "" {
			x, err := constraint.Parse(tf.GoBuildConstraint)
			if err != nil {
				return nil, fmt.Errorf("%s: parsing //go:build line: %v", tf.Name, err)
			}
			shouldBuild = eval(ctxt, x, allTags)
		} else if len(tf.PlusBuildConstraints) > 0 {
			for _, text := range tf.PlusBuildConstraints {
				if x, err := constraint.Parse(text); err == nil {
					if !eval(ctxt, x, allTags) {
						shouldBuild = false
					}
				}
			}
		}

		ext := nameExt(tf.Name)
		if !shouldBuild || tf.IgnoreFile {
			if strings.HasPrefix(tf.Name, "_") || strings.HasPrefix(tf.Name, ".") {
				// not due to build constraints - don't report
			} else if ext == ".go" {
				p.IgnoredGoFiles = append(p.IgnoredGoFiles, tf.Name)
			} else if fileListForExtBP(p, ext) != nil {
				p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, tf.Name)
			}
			continue
		}

		// Going to save the file. For non-Go files, can stop here.
		switch ext {
		case ".go":
			// keep going
		case ".S", ".sx":
			// special case for cgo, handled at end
			Sfiles = append(Sfiles, tf.Name)
			continue
		default:
			if list := fileListForExtBP(p, ext); list != nil {
				*list = append(*list, tf.Name)
			}
			continue
		}

		// TODO(matloob): determine pkg name here? pkg variable

		pkg := tf.PkgName
		if pkg == "documentation" {
			p.IgnoredGoFiles = append(p.IgnoredGoFiles, tf.Name)
			continue
		}
		isTest := strings.HasSuffix(tf.Name, "_test.go")
		isXTest := false
		if isTest && strings.HasSuffix(tf.PkgName, "_test") && p.Name != tf.PkgName {
			isXTest = true
			pkg = pkg[:len(pkg)-len("_test")]
		}

		// Grab the first package comment as docs, provided it is not from a test file.
		if tf.Doc != "" && p.Doc == "" && !isTest && !isXTest {
			p.Doc = tf.Doc
		}

		if p.Name == "" {
			p.Name = pkg
			firstFile = tf.Name
		} else if pkg != p.Name {
			// TODO(#45999): The choice of p.Name is arbitrary based on file iteration
			// order. Instead of resolving p.Name arbitrarily, we should clear out the
			// existing name and mark the existing files as also invalid.
			badFile(tf.Name, &MultiplePackageError{
				Dir:      p.Dir,
				Packages: []string{p.Name, pkg},
				Files:    []string{firstFile, tf.Name},
			})
		}

		if mode&ImportComment != 0 {
			com, err := strconv.Unquote(tf.QuotedImportComment)
			if err != nil {
				badFile(tf.Name, fmt.Errorf("%s:%d: cannot parse import comment", tf.Name, tf.QuotedImportCommentLine))
			} else if p.ImportComment == "" {
				p.ImportComment = com
				firstCommentFile = tf.Name
			} else if p.ImportComment != com {
				badFile(tf.Name, fmt.Errorf("found import comments %q (%s) and %q (%s) in %s", p.ImportComment, firstCommentFile, com, tf, p.Dir))
			}
		}

		// Record imports and information about cgo.
		isCgo := false
		for _, imp := range tf.Imports {
			if imp.Path == "C" {
				if isTest {
					badFile(tf.Name, fmt.Errorf("use of cgo in test %s not supported", tf.Name))
					continue
				}
				isCgo = true

				if imp.Doc != "" {
					if err := saveCgo(ctxt, tf.Name, p, imp.Doc); err != nil {
						badFile(tf.Name, err)
					}
				}

			}
		}

		var fileList *[]string
		var importMap, embedMap map[string][]token.Position
		switch {
		case isCgo:
			allTags["cgo"] = true
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

	p.EmbedPatterns, p.EmbedPatternPos = cleanDecls(embedPos)
	p.TestEmbedPatterns, p.TestEmbedPatternPos = cleanDecls(testEmbedPos)
	p.XTestEmbedPatterns, p.XTestEmbedPatternPos = cleanDecls(xTestEmbedPos)

	p.Imports, p.ImportPos = cleanDecls(importPos)
	p.TestImports, p.TestImportPos = cleanDecls(testImportPos)
	p.XTestImports, p.XTestImportPos = cleanDecls(xTestImportPos)

	for tag := range allTags {
		p.AllTags = append(p.AllTags, tag)
	}
	sort.Strings(p.AllTags)

	if len(p.CgoFiles) > 0 {
		p.SFiles = append(p.SFiles, Sfiles...)
		sort.Strings(p.SFiles)
	} else {
		p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, Sfiles...)
		sort.Strings(p.IgnoredOtherFiles)
	}
	// TODO Remove SFiles if we're not using cgo.

	if badGoError != nil {
		return p, badGoError
	}
	if len(p.GoFiles)+len(p.CgoFiles)+len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
		return p, &NoGoError{p.Dir}
	}
	return p, pkgerr
}

/////
///// TODO(matloob) delete all this stuff if we end up merging back into go/build

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

func saveCgo(ctxt build.Context, filename string, di *build.Package, importComment string) error {
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
				if matchAuto(ctxt, c, nil) {
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
			makePathsAbsolute(args, di.Dir)
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

func makePathsAbsolute(args []string, srcDir string) {
	nextPath := false
	for i, arg := range args {
		if nextPath {
			if !filepath.IsAbs(arg) {
				args[i] = filepath.Join(srcDir, arg)
			}
			nextPath = false
		} else if strings.HasPrefix(arg, "-I") || strings.HasPrefix(arg, "-L") {
			if len(arg) == 2 {
				nextPath = true
			} else {
				if !filepath.IsAbs(arg[2:]) {
					args[i] = arg[:2] + filepath.Join(srcDir, arg[2:])
				}
			}
		}
	}
}

// matchAuto interprets text as either a +build or //go:build expression (whichever works),
// reporting whether the expression matches the build context.
//
// matchAuto is only used for testing of tag evaluation
// and in #cgo lines, which accept either syntax.
func matchAuto(ctxt build.Context, text string, allTags map[string]bool) bool {
	if strings.ContainsAny(text, "&|()") {
		text = "//go:build " + text
	} else {
		text = "// +build " + text
	}
	x, err := constraint.Parse(text)
	if err != nil {
		return false
	}
	return eval(ctxt, x, allTags)
}

func eval(ctxt build.Context, x constraint.Expr, allTags map[string]bool) bool {
	return x.Eval(func(tag string) bool { return matchTag(ctxt, tag, allTags) })
}

// matchTag reports whether the name is one of:
//
//	cgo (if cgo is enabled)
//	$GOOS
//	$GOARCH
//	ctxt.Compiler
//	linux (if GOOS = android)
//	solaris (if GOOS = illumos)
//	tag (if tag is listed in ctxt.BuildTags or ctxt.ReleaseTags)
//
// It records all consulted tags in allTags.
func matchTag(ctxt build.Context, name string, allTags map[string]bool) bool {
	if allTags != nil {
		allTags[name] = true
	}

	// special tags
	if ctxt.CgoEnabled && name == "cgo" {
		return true
	}
	if name == ctxt.GOOS || name == ctxt.GOARCH || name == ctxt.Compiler {
		return true
	}
	if ctxt.GOOS == "android" && name == "linux" {
		return true
	}
	if ctxt.GOOS == "illumos" && name == "solaris" {
		return true
	}
	if ctxt.GOOS == "ios" && name == "darwin" {
		return true
	}

	// other tags
	for _, tag := range ctxt.BuildTags {
		if tag == name {
			return true
		}
	}
	for _, tag := range ctxt.ToolTags {
		if tag == name {
			return true
		}
	}
	for _, tag := range ctxt.ReleaseTags {
		if tag == name {
			return true
		}
	}

	return false
}

func goodOSArchFile(ctxt build.Context, name string, allTags map[string]bool) bool {
	name, _, _ = strings.Cut(name, ".")

	// Before Go 1.4, a file called "linux.go" would be equivalent to having a
	// build tag "linux" in that file. For Go 1.4 and beyond, we require this
	// auto-tagging to apply only to files with a non-empty prefix, so
	// "foo_linux.go" is tagged but "linux.go" is not. This allows new operating
	// systems, such as android, to arrive without breaking existing code with
	// innocuous source code in "android.go". The easiest fix: cut everything
	// in the name before the initial _.
	i := strings.Index(name, "_")
	if i < 0 {
		return true
	}
	name = name[i:] // ignore everything before first _

	l := strings.Split(name, "_")
	if n := len(l); n > 0 && l[n-1] == "test" {
		l = l[:n-1]
	}
	n := len(l)
	if n >= 2 && knownOS[l[n-2]] && knownArch[l[n-1]] {
		return matchTag(ctxt, l[n-1], allTags) && matchTag(ctxt, l[n-2], allTags)
	}
	if n >= 1 && (knownOS[l[n-1]] || knownArch[l[n-1]]) {
		return matchTag(ctxt, l[n-1], allTags)
	}
	return true
}
