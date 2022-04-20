package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"
)

type ModuleIndex struct {
	f        *os.File
	moddir   string
	st       *stringTable
	packages map[string]pkgInfo
}

type pkgInfo struct {
	dir        string
	offset     uint32
	rawPkgData *RawPackage2
}

func Open(path string, futurepath string) (mi *ModuleIndex, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	moddir := filepath.Dir(futurepath)

	mi = &ModuleIndex{f: f, moddir: moddir}

	defer func() {
		if e := recover(); e != nil {
			err = fmt.Errorf("error reading module index: %v", e)
		}
	}()

	// TODO(matloob): clean this up
	const indexv0 = "go index v0\n"
	var indexVersion [len(indexv0)]byte
	if _, err := io.ReadAtLeast(f, indexVersion[:], len(indexv0)); err != nil {
		return nil, err
	} else if string(indexVersion[:]) != indexv0 {
		return nil, fmt.Errorf("bad index version string: %q", string(indexVersion[:]))
	}
	stringTableOffset := mi.uint32()
	stbytes, err := mi.readStringTableBytes(stringTableOffset)
	if err != nil {
		return nil, err
	}
	mi.st = newStringTable(stbytes)
	numPackages := int(mi.uint32())

	pkgInfos := make([]pkgInfo, numPackages)

	for i := 0; i < numPackages; i++ {
		pkgInfos[i].dir = mi.st.String(mi.uint32())
	}
	for i := 0; i < numPackages; i++ {
		pkgInfos[i].offset = mi.uint32()
	}
	mi.packages = make(map[string]pkgInfo)
	for i := range pkgInfos {
		mi.packages[pkgInfos[i].dir] = pkgInfos[i]
	}

	return mi, nil
}

func (mi *ModuleIndex) RawPackage(path string) (*RawPackage2, bool) {
	pkgData, ok := mi.packages[path]
	if !ok {
		return nil, false
	}
	rp := new(RawPackage2)
	// TODO(matloob): do we want to lock on the module index?
	mi.f.Seek(int64(pkgData.offset), 0 /* relative to origin of file */)
	rp.Error = mi.string()
	rp.Path = mi.string()
	rp.SrcDir = mi.string()
	rp.Dir = mi.string()
	numSourceFiles := mi.uint32()
	rp.SourceFiles = make([]SourceFile, numSourceFiles)
	for i := uint32(0); i < numSourceFiles; i++ {
		rp.SourceFiles[i].mi = mi
		rp.SourceFiles[i].offset = mi.uint32()
	}
	return rp, true
}

type RawPackage2 struct {
	// TODO(matloob): Do we need AllTags in RawPackage?
	// We can produce it from contstraints when we evaluate them.

	Error string

	// Arguments to build.Import. Is path always "."?
	Path   string
	SrcDir string

	Dir string // directory containing package sources

	// Source files
	SourceFiles []SourceFile

	// No ConflictDir-- only relevant togopath
}

type SourceFile struct {
	mi *ModuleIndex // index file. TODO(matloob): make a specific decoder type?

	offset                                uint32
	savedImportsOffset, savedEmbedsOffset uint32

	// TODO(matloob): do we want to save the fields? I think no, because we probably don't
	// need to load the same package twice. We can always add it later.
}

const (
	sourceFileErrorOffset = 4 * iota
	sourceFileParseError
	sourceFileSynopsis
	sourceFileName
	sourceFilePkgName
	sourceFileIgnoreFile
	sourceFileBinaryOnly
	sourceFileQuotedImportComment
	sourceFileQuotedImportCommentLine
	sourceFileGoBuildConstraint
	sourceFileNumPlusBuildConstraints
)

func (sf *SourceFile) error() string {
	return sf.mi.stringAt(sf.offset + sourceFileErrorOffset)
}

func (sf *SourceFile) parseError() string {
	return sf.mi.stringAt(sf.offset + sourceFileParseError)
}

func (sf *SourceFile) name() string {
	return sf.mi.stringAt(sf.offset + sourceFileName)
}

func (sf *SourceFile) synopsis() string {
	return sf.mi.stringAt(sf.offset + sourceFileSynopsis)
}

func (sf *SourceFile) pkgName() string {
	return sf.mi.stringAt(sf.offset + sourceFilePkgName)
}

func (sf *SourceFile) ignoreFile() bool {
	return sf.mi.boolAt(sf.offset + sourceFileIgnoreFile)
}

func (sf *SourceFile) binaryOnly() bool {
	return sf.mi.boolAt(sf.offset + sourceFileBinaryOnly)
}

func (sf *SourceFile) quotedImportComment() string {
	return sf.mi.stringAt(sf.offset + sourceFileQuotedImportComment)
}

func (sf *SourceFile) quotedImportCommentLine() int {
	return int(sf.mi.uint32At(sf.offset + sourceFileQuotedImportCommentLine))
}

func (sf *SourceFile) goBuildConstraint() string {
	return sf.mi.stringAt(sf.offset + sourceFileGoBuildConstraint)
}

func (sf *SourceFile) plusBuildConstraints() []string {
	var ret []string

	d := decoderAt{sf.offset + sourceFileNumPlusBuildConstraints, sf.mi}
	n := int(d.uint32())
	for i := 0; i < n; i++ {
		ret = append(ret, d.string())
	}
	sf.savedImportsOffset = d.pos
	return ret
}

func (sf *SourceFile) importsOffset() uint32 {
	if sf.savedImportsOffset != 0 {
		return sf.savedImportsOffset
	}
	numPlusBuildConstraints := sf.mi.uint32At(sf.offset + sourceFileNumPlusBuildConstraints)
	sf.savedImportsOffset = sf.offset + sourceFileNumPlusBuildConstraints + 4*(numPlusBuildConstraints+1) // 4 bytes per uin32, add one to advance past numPlusBuildConstraints itself
	return sf.savedImportsOffset
}

func (sf *SourceFile) embedsOffset() uint32 {
	if sf.savedEmbedsOffset != 0 {
		return sf.savedEmbedsOffset
	}
	importsOffset := sf.importsOffset()
	numImports := sf.mi.uint32At(importsOffset)
	// 4 bytes per uint32; 1 to advance past numImports itself, and 6 uint32s per import
	sf.savedEmbedsOffset = importsOffset + 4*(1+(6*numImports))
	return sf.savedEmbedsOffset
}

func (sf *SourceFile) imports() []TFImport {
	var ret []TFImport

	importsOffset := sf.importsOffset()
	d := decoderAt{importsOffset, sf.mi}
	numImports := int(d.uint32())
	for i := 0; i < numImports; i++ {
		path := d.string()
		doc := d.string()
		pos := d.tokpos()
		ret = append(ret, TFImport{
			Path:     path,
			Doc:      doc, // TODO(matloob): only save for cgo?
			Position: pos,
		})
	}
	return ret
}

func (da *decoderAt) tokpos() token.Position {
	file := da.string()
	offset := int(da.uint32())
	line := int(da.uint32())
	column := int(da.uint32())
	return token.Position{
		Filename: file,
		Offset:   offset,
		Line:     line,
		Column:   column,
	}
}

func (sf *SourceFile) embeds() []embed {
	var ret []embed

	embedsOffset := sf.embedsOffset()
	d := decoderAt{embedsOffset, sf.mi}
	numEmbeds := int(d.uint32())
	for i := 0; i < numEmbeds; i++ {
		pattern := d.string()
		pos := d.tokpos()
		ret = append(ret, embed{pattern, pos})
	}
	return ret
}

type decoderAt struct {
	pos uint32
	mi  *ModuleIndex
}

func (da *decoderAt) uint32() uint32 {
	n := da.mi.uint32At(da.pos)
	da.pos += 4
	return n
}

func (da *decoderAt) string() string {
	s := da.mi.stringAt(da.pos)
	da.pos += 4
	return s
}

func (mi *ModuleIndex) uint32() uint32 {
	var n uint32
	if err := binary.Read(mi.f, binary.LittleEndian, &n); err != nil {
		panic(err)
	}
	return n
}

func (mi *ModuleIndex) uint32At(offset uint32) uint32 {
	b := make([]byte, 4)
	if _, err := mi.f.ReadAt(b, int64(offset)); err != nil {
		panic(err)
	}
	var n uint32
	if err := binary.Read(bytes.NewReader(b), binary.LittleEndian, &n); err != nil {
		panic(err)
	}
	return n
}

func (mi *ModuleIndex) boolAt(offset uint32) bool {
	switch v := mi.uint32At(offset); v {
	case 0:
		return false
	case 1:
		return true
	default:
		panic(fmt.Errorf("invalid bool value for SourceFile.IgnoreFile:", v))
	}
}

func (mi *ModuleIndex) Close() error {
	return mi.f.Close()
}

type stringTable struct {
	b       []byte
	strings map[uint32]string
}

func (mi *ModuleIndex) string() string {
	return mi.st.String(mi.uint32())
}

func (mi *ModuleIndex) stringAt(offset uint32) string {
	return mi.st.String(mi.uint32At(offset))
}

func (mi *ModuleIndex) readStringTableBytes(pos uint32) ([]byte, error) {
	st, err := mi.f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	stlen := int(size) - int(pos)
	buf := make([]byte, stlen)
	n, err := mi.f.ReadAt(buf, int64(pos))
	if err != nil && err != io.EOF {
		return nil, err
	} else if n != stlen {
		return nil, fmt.Errorf("did not read whole string table TODO should i keep reading?", n, stlen)
	}
	return buf, nil
}

// TODO(matloob): is it ok to read the entire string table? Should we read strings directly
// from the file?

func newStringTable(b []byte) *stringTable {
	return &stringTable{b: b, strings: make(map[uint32]string)}
}

func (st *stringTable) String(pos uint32) string {
	if pos == 0 {
		return ""
	}
	if s, ok := st.strings[pos]; ok {
		return s
	}
	var b bytes.Buffer
	i := int(pos)
	for ; i < len(st.b); i++ {
		c := st.b[i]
		if c == 0 {
			break
		}
		b.WriteByte(c)
	}
	if i == len(st.b) {
		panic("reached end of string table trying to read string")
	}
	s := b.String()
	st.strings[pos] = s
	return s
}
