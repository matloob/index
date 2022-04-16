package index

import (
	"bytes"
	"encoding/binary"
	"go/token"
	"io"
	"path/filepath"
	"sort"
)

// Todo(matloob) write straight to file? Much easier to poke in the
func EncodeModule(packages []*RawPackage, moddir string) ([]byte, error) {
	// fix up dir
	for i := range packages {
		rel, err := filepath.Rel(moddir, packages[i].Dir)
		if err != nil {
			return nil, err
		}
		packages[i].Dir = rel
	}

	e := newEncoder()
	e.Bytes([]byte("go index v0\n"))
	stringTableOffsetPos := e.Pos() // fill this at the end
	e.Uint32(0)                     // string table offset
	e.Uint32(uint32(len(packages)))
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Dir < packages[j].Dir
	})
	for _, p := range packages {
		e.String(p.SrcDir)
	}
	packagesOffsetPos := make([]uint32, len(packages))
	for i := range packages {
		packagesOffsetPos[i] = e.Pos()
		e.Uint32(0)
	}
	for i, p := range packages {
		e.Uint32At(e.Pos(), packagesOffsetPos[i])
		writePackage(e, p)
	}
	e.Uint32At(e.Pos(), stringTableOffsetPos)
	io.Copy(&e.buf, &e.stringTable)
	return e.buf.Bytes(), nil
}

func writePackage(e *encoder, p *RawPackage) {
	e.String(p.Error)
	e.String(p.Path)
	e.String(p.SrcDir)
	e.String(p.Dir)
	e.Uint32(uint32(len(p.SourceFiles)))                      // number of source files
	sourceFileOffsetPos := make([]uint32, len(p.SourceFiles)) // where to place the ith source file's offset
	for i := range p.SourceFiles {
		sourceFileOffsetPos[i] = e.Pos()
		e.Uint32(0)
	}
	for i, f := range p.SourceFiles {
		e.Uint32At(e.Pos(), sourceFileOffsetPos[i])
		writeSourceFile(e, f)
	}
}

func writeSourceFile(e *encoder, p *TaggedFile) {
	e.String(p.Error)
	e.String(p.ParseError)
	e.String(p.Synopsis)
	e.String(p.Name)
	e.String(p.PkgName)
	e.Bool(p.IgnoreFile)
	e.Bool(p.BinaryOnly)
	e.String(p.QuotedImportComment)
	e.Uint32(uint32(p.QuotedImportCommentLine))
	e.String(p.GoBuildConstraint)

	e.Uint32(uint32(len(p.PlusBuildConstraints)))
	for _, s := range p.PlusBuildConstraints {
		e.String(s)
	}

	e.Uint32(uint32(len(p.Imports)))
	for _, m := range p.Imports {
		e.String(m.Path)
		e.String(m.Doc) // TODO(matloob): only save for cgo?
		e.Position(m.Position)
	}
	// TODO(matloob) produce the slice earlier

	var embeds []embed
	for pattern, positions := range p.Embeds {
		for _, position := range positions {
			embeds = append(embeds, embed{pattern, position})
		}
	}
	e.Uint32(uint32(len(embeds)))
	for _, embed := range embeds {
		e.String(embed.pattern)
		e.Position(embed.position)

	}
}

type embed struct {
	pattern  string
	position token.Position
}

func newEncoder() *encoder {
	e := &encoder{strings: make(map[string]uint32)}

	// place the empty string at position 0 in the string table
	e.stringTable.WriteByte(0)
	e.strings[""] = 0

	return e
}

func (e *encoder) Position(position token.Position) {
	e.String(position.Filename)
	e.Uint32(uint32(position.Offset))
	e.Uint32(uint32(position.Line))
	e.Uint32(uint32(position.Column))
}

type encoder struct {
	buf         bytes.Buffer
	stringTable bytes.Buffer
	strings     map[string]uint32
}

func (e *encoder) Pos() uint32 {
	return uint32(e.buf.Len())
}

func (e *encoder) Bytes(b []byte) {
	e.buf.Write(b)
}

func (e *encoder) String(s string) {
	if n, ok := e.strings[s]; ok {
		e.Uint32(n)
		return
	}
	pos := uint32(e.stringTable.Len())
	e.strings[s] = pos
	e.Uint32(pos)
	e.stringTable.Write([]byte(s))
	e.stringTable.WriteByte(0)
}

func (e *encoder) Bool(b bool) {
	if b {
		e.Uint32(1)
	} else {
		e.Uint32(0)
	}
}

func (e *encoder) Uint32(n uint32) {
	binary.Write(&e.buf, binary.LittleEndian, n)
}

// There's got to be a better way to do this, right?
func (e *encoder) Uint32At(n uint32, at uint32) {
	buf := bytes.NewBuffer(make([]byte, 0, 4))
	binary.Write(buf, binary.LittleEndian, n)
	eb := e.buf.Bytes()
	for i := 0; i < 4; i++ {
		eb[int(at)+i] = buf.Bytes()[i]
	}
}
